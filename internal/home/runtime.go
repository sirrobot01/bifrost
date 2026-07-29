package home

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"slices"
	"sync"
	"time"

	"github.com/sirrobot01/bifrost/internal/certauto"
	"github.com/sirrobot01/bifrost/internal/config"
	"github.com/sirrobot01/bifrost/internal/diagnose"
	"github.com/sirrobot01/bifrost/internal/dnspublish"
	"github.com/sirrobot01/bifrost/internal/dockerwatch"
	"github.com/sirrobot01/bifrost/internal/edgeauth"
	"github.com/sirrobot01/bifrost/internal/exposure"
	"github.com/sirrobot01/bifrost/internal/hostfw"
	"github.com/sirrobot01/bifrost/internal/netwatch"
	"github.com/sirrobot01/bifrost/internal/notify"
	"github.com/sirrobot01/bifrost/internal/observability"
	platformapi "github.com/sirrobot01/bifrost/internal/platform"
	"github.com/sirrobot01/bifrost/internal/serviceaddr"
)

type Runtime struct {
	config       config.Config
	pinholes     *pinholeManager
	observer     netwatch.Observer
	controller   *Controller
	services     []Service
	publisher    *dnspublish.Reconciler
	certificates *certauto.Manager
	docker       *dockerwatch.Client
	verifier     *verifier
	notifier     notify.Notifier
	webhook      *notify.Webhook
	logger       *slog.Logger
	metrics      *observability.Server
	stateMu      sync.RWMutex
	startedAt    time.Time
	lastRun      time.Time
	lastError    string
	ready        bool
	lastPrefix   netip.Prefix
	// reloads carries a validated configuration into the run loop. Applying it
	// anywhere else would race the reconcile that is using the old one.
	reloads chan config.Config
}

func NewRuntime(configFile config.Config, logger *slog.Logger, host platformapi.Platform) (*Runtime, error) {
	if host == nil {
		return nil, errors.New("host platform is required")
	}
	if err := configFile.Validate(); err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	if len(configFile.StaticServices) == 0 && !configFile.Docker.Enabled {
		return nil, errors.New("at least one static service is required when Docker discovery is disabled")
	}

	observer, err := host.Observer(configFile.Interface)
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
	backend, err := host.AddressBackend(configFile.Interface)
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
		firewall, err = host.Firewall()
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
	var pinholes *pinholeManager
	if configFile.Firewall.PCP {
		gateway, gatewayErr := host.DefaultIPv6Gateway(configFile.Interface)
		if gatewayErr != nil {
			logger.Warn("router pinhole requests are unavailable", "error", gatewayErr)
		} else {
			pinholes, err = newPinholeManager(gateway, secret, logger)
		}
		if err != nil {
			logger.Warn("router pinhole requests are unavailable", "error", err)
		}
	}
	certificates, err := certauto.NewManager(certauto.Config{
		Provider:     provider,
		StateDir:     configFile.ACME.StateDir,
		Email:        configFile.ACME.Email,
		DirectoryURL: configFile.ACME.Directory,
		ChallengeTTL: configFile.DNS.TTL.Duration(),
		Wildcard:     configFile.ACME.Wildcard,
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
		Pinholes:           pinholeRequester(pinholes),
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
		dockerClient, err = host.DockerClient(configFile.Docker.Socket)
		if err != nil {
			return nil, err
		}
	}
	// Notifications reach an operator who is not watching a metrics endpoint,
	// which is most of them. Delivery is asynchronous so a slow endpoint can
	// never stall reconciliation.
	var notifier notify.Notifier = notify.Discard{}
	var webhook *notify.Webhook
	if configFile.Notify.Webhook != "" {
		webhook, err = notify.NewWebhook(notify.Config{
			Endpoint:    configFile.Notify.Webhook,
			Format:      configFile.Notify.Format,
			MinInterval: configFile.Notify.MinInterval.Duration(),
			Logger:      logger,
		})
		if err != nil {
			return nil, err
		}
		notifier = webhook
	}

	// The daemon keeps asking the question `check` only answers during setup.
	// Without a prober there is no evidence and therefore no verdict; the
	// verifier reports nothing rather than something reassuring.
	var externalVerifier *verifier
	if configFile.VerificationEnabled() {
		edgeAddresses := make([]netip.Addr, 0, len(configFile.Edge.IPv4Addresses))
		if configFile.Edge.Enabled {
			for _, raw := range configFile.Edge.IPv4Addresses {
				address, parseErr := netip.ParseAddr(raw)
				if parseErr != nil {
					return nil, fmt.Errorf("edge.ipv4_addresses %q: %w", raw, parseErr)
				}
				edgeAddresses = append(edgeAddresses, address)
			}
		}
		prober, proberErr := diagnose.SelectProber(configFile.Probe.Endpoint, edgeAddresses)
		if proberErr != nil {
			return nil, proberErr
		}
		if prober == nil {
			logger.Warn("no external prober is configured, so reachability cannot be verified from outside this host",
				"fix", "enable an edge or set probe.endpoint")
		}
		externalVerifier = newVerifier(prober, notifier, logger)
	}

	runtime := &Runtime{config: configFile, pinholes: pinholes, observer: observer, controller: controller, services: services, publisher: publisher, certificates: certificates, docker: dockerClient, verifier: externalVerifier, notifier: notifier, webhook: webhook, logger: logger, startedAt: time.Now()}
	metricsServer, err := observability.NewServer(configFile.Metrics.Listen, runtime.observabilitySnapshot)
	if err != nil {
		return nil, err
	}
	runtime.metrics = metricsServer
	runtime.reloads = make(chan config.Config, 1)
	return runtime, nil
}

// Reload replaces the running configuration with next. It rejects a change that
// cannot be applied in place rather than applying part of it, so a daemon never
// ends up disagreeing with its own file.
func (r *Runtime) Reload(next config.Config) error {
	next.ApplyDefaults()
	if err := next.Validate(); err != nil {
		return err
	}
	r.stateMu.RLock()
	current := r.config
	r.stateMu.RUnlock()
	if err := current.Reloadable(next); err != nil {
		return err
	}
	select {
	case r.reloads <- next:
		return nil
	default:
		return errors.New("a reload is already pending")
	}
}

// applyReload swaps the configuration in and returns whether the published
// services need reconciling.
func (r *Runtime) applyReload(ctx context.Context, next config.Config) bool {
	r.stateMu.Lock()
	previous := r.config
	r.config = next
	r.stateMu.Unlock()

	r.controller.Reconfigure(next.DNS.TTL.Duration(), next.DrainGrace.Duration(), hostfw.Spec{
		TrustedInterfaces: next.Firewall.TrustedInterfaces,
		AllowPorts:        next.Firewall.AllowPorts,
	})
	if err := r.refreshServices(ctx); err != nil {
		r.logger.Error("reload could not rebuild the service list", "error", err)
		// The previous list is still live and correct, so keep serving it.
		r.stateMu.Lock()
		r.config = previous
		r.stateMu.Unlock()
		return false
	}
	r.logger.Info("reloaded configuration", "services", len(r.services))
	return true
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
	if r.webhook != nil {
		go func() { _ = r.webhook.Run(ctx) }()
	}
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
	// The first check runs one interval in, which gives DNS and the edge time
	// to converge after a start. Probing immediately would report an outage
	// that is really just a cold start.
	externalCheck := time.NewTicker(r.config.Verify.Interval.Duration())
	defer externalCheck.Stop()
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
		case next := <-r.reloads:
			if !r.applyReload(ctx, next) {
				continue
			}
			externalCheck.Reset(r.config.Verify.Interval.Duration())
			if latest.InterfaceName != "" {
				pending = true
				consecutiveFailures = 0
				if !settleTimer.Stop() {
					select {
					case <-settleTimer.C:
					default:
					}
				}
				// A reload is an operator action, so converge promptly rather
				// than waiting out a settle window meant for network churn.
				settleTimer.Reset(time.Millisecond)
			}
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
				// Renewal begins 30 days before expiry, so this has weeks of
				// headroom. Saying so now is the difference between a warning
				// and an outage.
				r.logger.Error("certificate renewal failed", "error", err)
				r.notifier.Notify(notify.Event{
					Kind:    notify.CertificateRenewalFailed,
					Summary: "certificate renewal failed",
					Detail:  err.Error(),
				})
			}
		case <-externalCheck.C:
			if !pending {
				r.verifier.Verify(ctx, r.verifyTargets())
			}
		case <-settleTimer.C:
			if pending {
				if err := r.reconcile(ctx, latest); err != nil {
					consecutiveFailures++
					delay := reconcileBackoff(r.config.SettleWindow.Duration(), consecutiveFailures)
					r.markReconcile(err)
					r.logger.Error("service reconciliation failed", "error", err, "retry_in", delay.String())
					r.notifier.Notify(notify.Event{
						Kind:    notify.ReconcileFailed,
						Summary: "service reconciliation failed",
						Detail:  err.Error(),
					})
					settleTimer.Reset(delay)
					continue
				}
				consecutiveFailures = 0
				r.markReconcile(nil)
				pending = false
			}
		case <-sweep.C:
			if r.pinholes != nil {
				r.pinholes.Renew(ctx)
			}
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

// verifyTargets is the published state as it currently stands, which is what an
// outside vantage would be reaching. It deliberately reads the controller
// rather than the configuration: a service that failed to come up has no
// address to probe, and reporting it unreachable would blame the network for a
// local failure the reconcile error already covers.
func (r *Runtime) verifyTargets() []verifyTarget {
	statuses := r.controller.Status()
	targets := make([]verifyTarget, 0, len(statuses))
	for _, status := range statuses {
		for _, address := range status.Addresses {
			targets = append(targets, verifyTarget{
				Service: status.ID,
				DNSName: status.DNSName,
				Address: address,
				Port:    status.ListenPort,
			})
			// One address is enough to prove the path. Probing every address
			// during a renumbering overlap would report the retiring one as a
			// failure while it is being drained on purpose.
			break
		}
	}
	return targets
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
	for _, state := range r.verifier.States() {
		snapshot.Reachability = append(snapshot.Reachability, observability.Reachability{
			Service:   state.Service,
			Reachable: state.Reachable,
			Detail:    state.Detail,
			CheckedAt: state.CheckedAt,
		})
	}
	slices.SortFunc(snapshot.Reachability, func(a, b observability.Reachability) int { return cmp.Compare(a.Service, b.Service) })
	if r.certificates != nil {
		for name, expiry := range r.certificates.Expiries() {
			snapshot.Certificates = append(snapshot.Certificates, observability.Certificate{Name: name, NotAfter: expiry})
		}
		slices.SortFunc(snapshot.Certificates, func(a, b observability.Certificate) int { return cmp.Compare(a.Name, b.Name) })
	}
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
	r.notePrefix()
	return nil
}

// notePrefix reports a prefix change once. It is worth a notification because
// it explains anything that follows it: a renumbering bounded by a provider's
// minimum TTL looks like an unexplained outage without this line.
func (r *Runtime) notePrefix() {
	current := r.controller.SelectedPrefix()
	if !current.IsValid() {
		return
	}
	r.stateMu.Lock()
	previous := r.lastPrefix
	r.lastPrefix = current
	r.stateMu.Unlock()
	if !previous.IsValid() || previous == current {
		return
	}
	r.logger.Info("published prefix changed", "from", previous.String(), "to", current.String())
	r.notifier.Notify(notify.Event{
		Kind:    notify.PrefixChanged,
		Summary: "the published IPv6 prefix changed",
		Detail:  previous.String() + " to " + current.String(),
	})
}

func (r *Runtime) logActions(actions []Action) {
	for _, action := range actions {
		r.logger.Info("reconciled service", "service", action.Service, "action", action.Kind, "detail", action.Detail)
	}
}
