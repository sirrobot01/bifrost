//go:build linux

package home

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"sync"
	"time"

	"github.com/sirrobot01/bifrost/internal/certauto"
	"github.com/sirrobot01/bifrost/internal/config"
	"github.com/sirrobot01/bifrost/internal/dnspublish"
	"github.com/sirrobot01/bifrost/internal/dockerwatch"
	"github.com/sirrobot01/bifrost/internal/edgeauth"
	"github.com/sirrobot01/bifrost/internal/exposure"
	"github.com/sirrobot01/bifrost/internal/hostfw"
	"github.com/sirrobot01/bifrost/internal/netwatch"
	"github.com/sirrobot01/bifrost/internal/observability"
	"github.com/sirrobot01/bifrost/internal/serviceaddr"
)

type Runtime struct {
	config       config.Config
	observer     *netwatch.Observer
	controller   *Controller
	services     []Service
	publisher    *dnspublish.Reconciler
	certificates *certauto.Manager
	docker       *dockerwatch.Client
	logger       *slog.Logger
	metrics      *observability.Server
	stateMu      sync.RWMutex
	startedAt    time.Time
	lastRun      time.Time
	lastError    string
	ready        bool
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
	provider, err := dnspublish.NewProvider(configFile.DNS)
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
	globalLimiter, err := exposure.NewLimiter(8192)
	if err != nil {
		return nil, err
	}
	var edgeVerifier *edgeauth.Verifier
	if configFile.Edge.Enabled {
		edgeKey, err := config.ReadSecret(configFile.Edge.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("edge key: %w", err)
		}
		edgeVerifier, err = edgeauth.NewVerifier(edgeKey, configFile.Edge.MaxClockSkew.Duration())
		if err != nil {
			return nil, err
		}
	}
	var firewall hostfw.Manager
	var allowances hostfw.Spec
	if configFile.Firewall.Managed() {
		firewall, err = hostfw.New()
		if err != nil {
			return nil, fmt.Errorf("managed firewall: %w", err)
		}
		allowances = hostfw.Spec{
			TrustedInterfaces: configFile.Firewall.TrustedInterfaces,
			AllowPorts:        configFile.Firewall.AllowPorts,
		}
		logger.Warn("managed firewall mode is enabled: inbound IPv6 will be dropped except for published services and configured allowances",
			"trusted_interfaces", configFile.Firewall.TrustedInterfaces, "allow_ports", configFile.Firewall.AllowPorts)
	}
	certificates, err := certauto.NewManager(certauto.Config{
		Provider:     provider,
		StateDir:     configFile.ACME.StateDir,
		Email:        configFile.ACME.Email,
		DirectoryURL: configFile.ACME.Directory,
		ChallengeTTL: configFile.DNS.TTL.Duration(),
		Logger:       logger,
	})
	if err != nil {
		return nil, fmt.Errorf("certificate manager: %w", err)
	}
	controller, err := NewController(ControllerConfig{
		Addresses:          addressManager,
		Deriver:            deriver,
		Publisher:          publisher,
		Certificates:       certificates,
		Firewall:           firewall,
		FirewallAllowances: allowances,
		Listen: func(ctx context.Context, listenerConfig exposure.Config) (Splicer, error) {
			return exposure.Listen(ctx, listenerConfig)
		},
		TTL:               configFile.DNS.TTL.Duration(),
		DrainGrace:        configFile.DrainGrace.Duration(),
		DialTimeout:       10 * time.Second,
		IdleTimeout:       5 * time.Minute,
		Logger:            logger,
		PrefixOverride:    prefixOverride,
		GlobalLimiter:     globalLimiter,
		EdgeVerifier:      edgeVerifier,
		EdgeHeaderTimeout: configFile.Edge.HeaderTimeout.Duration(),
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
	runtime := &Runtime{config: configFile, observer: observer, controller: controller, services: services, publisher: publisher, certificates: certificates, docker: dockerClient, logger: logger, startedAt: time.Now()}
	metricsServer, err := observability.NewServer(configFile.Metrics.Listen, runtime.observabilitySnapshot)
	if err != nil {
		return nil, err
	}
	runtime.metrics = metricsServer
	return runtime, nil
}

func (r *Runtime) DryRun(ctx context.Context) ([]Action, error) {
	if err := r.refreshServices(ctx); err != nil {
		return nil, err
	}
	snapshot, err := r.observer.Snapshot()
	if err != nil {
		return nil, err
	}
	actions, err := r.controller.Reconcile(ctx, r.services, snapshot, true)
	if err != nil {
		return nil, err
	}
	// One authenticated read per service proves the provider credentials,
	// zone, and record ownership. Without it a wrong zone prints a plausible
	// plan here and then fails on the first live reconcile.
	verified := make(map[string]struct{}, len(r.services))
	for _, service := range r.services {
		if _, done := verified[service.DNSName]; done {
			continue
		}
		verified[service.DNSName] = struct{}{}
		if err := r.publisher.Preflight(ctx, service.DNSName); err != nil {
			return nil, fmt.Errorf("provider preflight for %s: %w", service.DNSName, err)
		}
		actions = append(actions, Action{Service: service.ID, Kind: "verify", Detail: "provider credentials and DNS ownership verified for " + service.DNSName})
	}
	return actions, nil
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
	metricsResult := make(chan error, 1)
	go func() { metricsResult <- r.metrics.Run(ctx) }()
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
	certificateRenewal := time.NewTicker(time.Hour)
	defer certificateRenewal.Stop()
	var latest netwatch.Snapshot
	pending := false
	consecutiveFailures := 0

	for {
		select {
		case <-ctx.Done():
			// The drain consumes up to drain_grace; the margin keeps the DNS
			// withdraws and address removals from being starved by it.
			shutdownContext, cancel := context.WithTimeout(context.Background(), r.config.DrainGrace.Duration()+30*time.Second)
			defer cancel()
			if err := r.controller.Shutdown(shutdownContext); err != nil {
				// The next start reconciles provider state from scratch, so an
				// incomplete withdraw must not turn a routine stop into a
				// failed unit.
				r.logger.Error("shutdown left provider state behind", "error", err)
			}
			return nil
		case err := <-observerResult:
			if ctx.Err() != nil {
				continue
			}
			return err
		case err := <-metricsResult:
			if ctx.Err() != nil {
				continue
			}
			return fmt.Errorf("observability server: %w", err)
		case snapshot := <-snapshots:
			latest = snapshot
			pending = true
			// New network state is new information: retry promptly even when
			// earlier reconciles were failing.
			consecutiveFailures = 0
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
				consecutiveFailures = 0
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
		case <-certificateRenewal.C:
			renewed, err := r.certificates.RenewDue(ctx)
			for _, name := range renewed {
				r.logger.Info("renewed certificate", "name", name)
			}
			if err != nil {
				r.logger.Error("certificate renewal failed", "error", err)
			}
		case <-settleTimer.C:
			if pending {
				if err := r.reconcile(ctx, latest); err != nil {
					consecutiveFailures++
					delay := reconcileBackoff(r.config.SettleWindow.Duration(), consecutiveFailures)
					r.markReconcile(err)
					r.logger.Error("service reconciliation failed", "error", err, "retry_in", delay.String())
					settleTimer.Reset(delay)
					continue
				}
				consecutiveFailures = 0
				r.markReconcile(nil)
				pending = false
			}
		case <-sweep.C:
			if !pending {
				actions, err := r.controller.Sweep(ctx)
				if err != nil {
					r.markReconcile(err)
					r.logger.Error("service retirement failed", "error", err)
					continue
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

func (r *Runtime) Status() []ServiceStatus {
	return r.controller.Status()
}

func (r *Runtime) observabilitySnapshot() observability.Snapshot {
	r.stateMu.RLock()
	snapshot := observability.Snapshot{Ready: r.ready, StartedAt: r.startedAt, LastReconcile: r.lastRun, LastError: r.lastError}
	r.stateMu.RUnlock()
	for _, status := range r.controller.Status() {
		snapshot.Services = append(snapshot.Services, observability.Service{
			ID:                status.ID,
			DNSName:           status.DNSName,
			Mode:              string(status.Mode),
			Addresses:         status.Addresses,
			EdgeAddresses:     status.EdgeAddresses,
			Backend:           status.Backend,
			ClientIPPreserved: status.ClientIPPreserved,
			ActiveConnections: status.ActiveConnections,
			Accepted:          status.Accepted,
			Rejected:          status.Rejected,
			DialFailures:      status.DialFailures,
		})
	}
	return snapshot
}

func (r *Runtime) markReconcile(err error) {
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	r.lastRun = time.Now()
	if err != nil {
		r.ready = false
		r.lastError = err.Error()
		return
	}
	r.ready = true
	r.lastError = ""
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
