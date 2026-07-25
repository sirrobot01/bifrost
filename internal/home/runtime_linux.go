//go:build linux

package home

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"time"

	"github.com/sirrobot01/bifrost/internal/config"
	"github.com/sirrobot01/bifrost/internal/dnspublish"
	"github.com/sirrobot01/bifrost/internal/dockerwatch"
	"github.com/sirrobot01/bifrost/internal/exposure"
	"github.com/sirrobot01/bifrost/internal/netwatch"
	"github.com/sirrobot01/bifrost/internal/serviceaddr"
)

type Runtime struct {
	config     config.Config
	observer   *netwatch.Observer
	controller *Controller
	services   []Service
	docker     *dockerwatch.Client
	logger     *slog.Logger
}

func NewRuntime(configFile config.Config, logger *slog.Logger) (*Runtime, error) {
	if err := configFile.Validate(); err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	if len(configFile.StaticServices) == 0 && !configFile.Docker.Enabled {
		return nil, errors.New("at least one static service is required when Docker discovery is disabled")
	}

	observer, err := netwatch.New(configFile.Interface)
	if err != nil {
		return nil, err
	}
	secret, err := config.ReadSecret(configFile.SecretFile)
	if err != nil {
		return nil, fmt.Errorf("address derivation secret: %w", err)
	}
	deriver, err := serviceaddr.NewDeriver(secret)
	if err != nil {
		return nil, err
	}
	backend, err := serviceaddr.NewNetlinkBackend(configFile.Interface)
	if err != nil {
		return nil, err
	}
	addressManager, err := serviceaddr.NewManager(backend, deriver)
	if err != nil {
		return nil, err
	}
	provider, err := providerFromConfig(configFile)
	if err != nil {
		return nil, err
	}
	publisher, err := dnspublish.NewReconciler(provider, configFile.OwnerID)
	if err != nil {
		return nil, err
	}
	services, err := servicesFromConfig(configFile)
	if err != nil {
		return nil, err
	}
	var prefixOverride netip.Prefix
	if configFile.PrefixOverride != "" {
		prefixOverride = netip.MustParsePrefix(configFile.PrefixOverride)
	}
	controller, err := NewController(ControllerConfig{
		Addresses: addressManager,
		Deriver:   deriver,
		Publisher: publisher,
		Listen: func(ctx context.Context, listenerConfig exposure.Config) (Splicer, error) {
			return exposure.Listen(ctx, listenerConfig)
		},
		TTL:            configFile.DNS.TTL.Duration(),
		DrainGrace:     configFile.DrainGrace.Duration(),
		DialTimeout:    10 * time.Second,
		IdleTimeout:    5 * time.Minute,
		Logger:         logger,
		PrefixOverride: prefixOverride,
	})
	if err != nil {
		return nil, err
	}
	var dockerClient *dockerwatch.Client
	if configFile.Docker.Enabled {
		dockerClient, err = dockerwatch.NewClient(dockerwatch.ClientConfig{Socket: configFile.Docker.Socket})
		if err != nil {
			return nil, err
		}
	}
	return &Runtime{config: configFile, observer: observer, controller: controller, services: services, docker: dockerClient, logger: logger}, nil
}

func (r *Runtime) DryRun(ctx context.Context) ([]Action, error) {
	if err := r.refreshServices(ctx); err != nil {
		return nil, err
	}
	snapshot, err := r.observer.Snapshot()
	if err != nil {
		return nil, err
	}
	return r.controller.Reconcile(ctx, r.services, snapshot, true)
}

func (r *Runtime) Run(ctx context.Context) error {
	if err := r.refreshServices(ctx); err != nil {
		return err
	}
	snapshots := make(chan netwatch.Snapshot, 16)
	observerResult := make(chan error, 1)
	go func() {
		observerResult <- r.observer.Observe(ctx, snapshots)
	}()
	dockerChanges := make(chan struct{}, 1)
	if r.docker != nil {
		go r.watchDocker(ctx, dockerChanges)
	}

	settleTimer := time.NewTimer(r.config.SettleWindow.Duration())
	if !settleTimer.Stop() {
		<-settleTimer.C
	}
	defer settleTimer.Stop()
	sweep := time.NewTicker(time.Second)
	defer sweep.Stop()
	dockerResync := time.NewTicker(30 * time.Second)
	defer dockerResync.Stop()
	var latest netwatch.Snapshot
	pending := false

	for {
		select {
		case <-ctx.Done():
			shutdownContext, cancel := context.WithTimeout(context.Background(), r.config.DrainGrace.Duration())
			defer cancel()
			return r.controller.Shutdown(shutdownContext)
		case err := <-observerResult:
			if ctx.Err() != nil {
				continue
			}
			return err
		case snapshot := <-snapshots:
			latest = snapshot
			pending = true
			if !settleTimer.Stop() {
				select {
				case <-settleTimer.C:
				default:
				}
			}
			settleTimer.Reset(r.config.SettleWindow.Duration())
		case <-dockerChanges:
			if err := r.refreshServices(ctx); err != nil {
				r.logger.Error("Docker reconciliation failed", "error", err)
				continue
			}
			if latest.InterfaceName != "" {
				pending = true
				if !settleTimer.Stop() {
					select {
					case <-settleTimer.C:
					default:
					}
				}
				settleTimer.Reset(r.config.SettleWindow.Duration())
			}
		case <-dockerResync.C:
			if r.docker != nil {
				select {
				case dockerChanges <- struct{}{}:
				default:
				}
			}
		case <-settleTimer.C:
			if pending {
				if err := r.reconcile(ctx, latest); err != nil {
					return err
				}
				pending = false
			}
		case <-sweep.C:
			if !pending {
				actions, err := r.controller.Sweep(ctx)
				if err != nil {
					return err
				}
				r.logActions(actions)
			}
		}
	}
}

func (r *Runtime) refreshServices(ctx context.Context) error {
	configured := append([]config.StaticService(nil), r.config.StaticServices...)
	if r.docker != nil {
		discovered, err := r.docker.ListServices(ctx)
		if err != nil {
			return fmt.Errorf("list Docker services: %w", err)
		}
		configured = append(configured, discovered...)
	}
	configFile := r.config
	configFile.StaticServices = configured
	services, err := servicesFromConfig(configFile)
	if err != nil {
		return err
	}
	r.services = services
	return nil
}

func (r *Runtime) watchDocker(ctx context.Context, changes chan<- struct{}) {
	for ctx.Err() == nil {
		err := r.docker.Watch(ctx, changes)
		if ctx.Err() != nil {
			return
		}
		r.logger.Warn("Docker event stream disconnected", "error", err)
		select {
		case changes <- struct{}{}:
		default:
		}
		timer := time.NewTimer(2 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func providerFromConfig(configFile config.Config) (dnspublish.Provider, error) {
	switch configFile.DNS.Provider {
	case "cloudflare":
		token, err := config.ReadSecret(configFile.DNS.Cloudflare.APITokenFile)
		if err != nil {
			return nil, fmt.Errorf("cloudflare token: %w", err)
		}
		return dnspublish.NewCloudflare(dnspublish.CloudflareConfig{ZoneID: configFile.DNS.Cloudflare.ZoneID, APIToken: string(token)})
	case "desec":
		token, err := config.ReadSecret(configFile.DNS.DESEC.TokenFile)
		if err != nil {
			return nil, fmt.Errorf("deSEC token: %w", err)
		}
		return dnspublish.NewDESEC(dnspublish.DESECConfig{Zone: configFile.DNS.DESEC.Zone, Token: string(token)})
	case "dynv6":
		token, err := config.ReadSecret(configFile.DNS.Dynv6.TokenFile)
		if err != nil {
			return nil, fmt.Errorf("dynv6 token: %w", err)
		}
		return dnspublish.NewDynv6(dnspublish.Dynv6Config{Zone: configFile.DNS.Dynv6.Zone, Token: string(token)})
	case "rfc2136":
		secret := ""
		if configFile.DNS.RFC2136.KeyFile != "" {
			key, err := config.ReadSecret(configFile.DNS.RFC2136.KeyFile)
			if err != nil {
				return nil, fmt.Errorf("rfc 2136 key: %w", err)
			}
			secret = string(key)
		}
		return dnspublish.NewRFC2136(dnspublish.RFC2136Config{Server: configFile.DNS.RFC2136.Server, Zone: configFile.DNS.RFC2136.Zone, KeyName: configFile.DNS.RFC2136.KeyName, KeySecret: secret, Algorithm: configFile.DNS.RFC2136.Algorithm})
	default:
		return nil, fmt.Errorf("unsupported DNS provider %q", configFile.DNS.Provider)
	}
}

func (r *Runtime) Status() []ServiceStatus {
	return r.controller.Status()
}

func (r *Runtime) reconcile(ctx context.Context, snapshot netwatch.Snapshot) error {
	actions, err := r.controller.Reconcile(ctx, r.services, snapshot, false)
	if err != nil {
		return err
	}
	r.logActions(actions)
	return nil
}

func (r *Runtime) logActions(actions []Action) {
	for _, action := range actions {
		r.logger.Info("reconciled service", "service", action.Service, "action", action.Kind, "detail", action.Detail)
	}
}
