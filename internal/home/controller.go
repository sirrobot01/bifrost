package home

import (
	"cmp"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sirrobot01/bifrost/internal/dnspublish"
	"github.com/sirrobot01/bifrost/internal/edgeauth"
	"github.com/sirrobot01/bifrost/internal/exposure"
	"github.com/sirrobot01/bifrost/internal/hostfw"
	"github.com/sirrobot01/bifrost/internal/netwatch"
	"github.com/sirrobot01/bifrost/internal/serviceaddr"
)

type AddressManager interface {
	Ensure(context.Context, netip.Prefix, string) (serviceaddr.Lease, error)
	Remove(serviceaddr.Lease) error
}

type Publisher interface {
	Ensure(context.Context, dnspublish.Publication) error
	Withdraw(context.Context, string) error
	Prune(context.Context, []string) error
}

type Splicer interface {
	Address() netip.AddrPort
	Serve(context.Context) error
	Shutdown(context.Context) error
	Status() exposure.Status
}

type ListenerFactory func(context.Context, exposure.Config) (Splicer, error)

// CertificateSource issues and serves certificates for splice services that
// terminate TLS.
type CertificateSource interface {
	// Certificate blocks until a usable certificate for name exists.
	Certificate(ctx context.Context, name string) (*tls.Certificate, error)
	// TLSConfig serves name's current certificate and follows renewals.
	TLSConfig(name string) *tls.Config
}

type Action struct {
	Service string `json:"service"`
	Kind    string `json:"kind"`
	Detail  string `json:"detail"`
}

type ServiceStatus struct {
	ID                string         `json:"id"`
	DNSName           string         `json:"dns"`
	Mode              Mode           `json:"mode"`
	Addresses         []netip.Addr   `json:"addresses"`
	EdgeAddresses     []netip.Addr   `json:"edge_addresses,omitempty"`
	Backend           netip.AddrPort `json:"backend"`
	ClientIPPreserved bool           `json:"client_ip_preserved"`
	ActiveConnections int64          `json:"active_connections"`
	Accepted          uint64         `json:"accepted_total"`
	Rejected          uint64         `json:"rejected_total"`
	DialFailures      uint64         `json:"dial_failures_total"`
}

type ControllerConfig struct {
	Addresses   AddressManager
	Deriver     serviceaddr.Deriver
	Publisher   Publisher
	Listen      ListenerFactory
	TTL         time.Duration
	DrainGrace  time.Duration
	DialTimeout time.Duration
	IdleTimeout time.Duration
	Logger      *slog.Logger
	Now         func() time.Time
	CheckDirect func(context.Context, netip.AddrPort) error
	// Certificates enables TLS termination for services that ask for it.
	Certificates CertificateSource
	// Firewall, when set, makes Bifrost authoritative for inbound IPv6: the
	// managed table accepts the published sockets and drops the rest.
	Firewall hostfw.Manager
	// FirewallAllowances are the operator's extra accepts, applied alongside
	// the published sockets.
	FirewallAllowances hostfw.Spec
	PrefixOverride     netip.Prefix
	GlobalLimiter      *exposure.Limiter
	EdgeVerifier       *edgeauth.Verifier
	EdgeHeaderTimeout  time.Duration
}

type Controller struct {
	config   ControllerConfig
	mu       sync.RWMutex
	services map[string]*serviceState
	// status is a lock-free snapshot for observability. Reconcile can hold mu
	// for minutes (certificate issuance, provider calls); status readers must
	// never wait behind it.
	status atomic.Pointer[[]statusEntry]
	// appliedFirewall is the last spec written to the host, so an unchanged
	// policy is not rewritten on every reconcile.
	appliedFirewall hostfw.Spec
	firewallApplied bool
}

// applyFirewall writes the managed policy for the services in hand plus any
// additional endpoints about to be prepared. It runs before publication so a
// name never resolves to an address whose port is still dropped. Must be
// called with mu held.
func (c *Controller) applyFirewall(ctx context.Context, pending []hostfw.Endpoint) error {
	if c.config.Firewall == nil {
		return nil
	}
	spec := hostfw.Spec{
		Endpoints:         pending,
		TrustedInterfaces: c.config.FirewallAllowances.TrustedInterfaces,
		AllowPorts:        c.config.FirewallAllowances.AllowPorts,
	}
	for _, state := range c.services {
		for _, endpoint := range state.endpoints {
			if endpoint.splicer == nil {
				continue
			}
			spec.Endpoints = append(spec.Endpoints, hostfw.Endpoint{Service: state.spec.ID, Address: endpoint.address, Port: state.spec.ListenPort})
		}
	}
	if c.firewallApplied && spec.Equal(c.appliedFirewall) {
		return nil
	}
	if err := c.config.Firewall.Apply(ctx, spec); err != nil {
		return err
	}
	c.config.Logger.Info("applied managed firewall policy", "policy", spec.Describe())
	c.appliedFirewall = spec
	c.firewallApplied = true
	return nil
}

// statusEntry is an immutable copy of one service's shape. Live counters come
// from the splicers' atomics at read time.
type statusEntry struct {
	spec      Service
	mode      Mode
	endpoints []statusEndpoint
}

type statusEndpoint struct {
	address netip.Addr
	splicer Splicer
}

// refreshStatus must run with mu held. It publishes an immutable snapshot for
// Status readers.
func (c *Controller) refreshStatus() {
	entries := make([]statusEntry, 0, len(c.services))
	for _, state := range c.services {
		entry := statusEntry{spec: state.spec, mode: state.mode, endpoints: make([]statusEndpoint, 0, len(state.endpoints))}
		for _, endpoint := range state.endpoints {
			entry.endpoints = append(entry.endpoints, statusEndpoint{address: endpoint.address, splicer: endpoint.splicer})
		}
		entries = append(entries, entry)
	}
	c.status.Store(&entries)
}

type serviceState struct {
	spec      Service
	mode      Mode
	endpoints []*endpoint
}

type endpoint struct {
	address  netip.Addr
	lease    *serviceaddr.Lease
	splicer  Splicer
	retireAt time.Time
}

func NewController(config ControllerConfig) (*Controller, error) {
	if config.Addresses == nil || config.Publisher == nil || config.Listen == nil {
		return nil, errors.New("address manager, DNS publisher, and listener factory are required")
	}
	if config.TTL < 60*time.Second || config.DrainGrace <= 0 || config.DialTimeout <= 0 || config.IdleTimeout <= 0 {
		return nil, errors.New("TTL, drain grace, dial timeout, and idle timeout must be valid")
	}
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.CheckDirect == nil {
		dialer := &net.Dialer{Timeout: config.DialTimeout}
		config.CheckDirect = func(ctx context.Context, address netip.AddrPort) error {
			connection, err := dialer.DialContext(ctx, "tcp6", address.String())
			if err != nil {
				return err
			}
			return connection.Close()
		}
	}
	controller := &Controller{config: config, services: make(map[string]*serviceState)}
	controller.refreshStatus()
	return controller, nil
}

func (c *Controller) Reconcile(ctx context.Context, desired []Service, snapshot netwatch.Snapshot, dryRun bool) ([]Action, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	defer c.refreshStatus()

	indexed := make(map[string]Service, len(desired))
	desiredDNSNames := make([]string, 0, len(desired))
	for _, service := range desired {
		if err := service.validate(); err != nil {
			return nil, fmt.Errorf("service %q: %w", service.ID, err)
		}
		if service.Edge && c.config.EdgeVerifier == nil {
			return nil, fmt.Errorf("service %q: edge authentication verifier is required", service.ID)
		}
		if _, exists := indexed[service.ID]; exists {
			return nil, fmt.Errorf("duplicate service %q", service.ID)
		}
		indexed[service.ID] = service
		desiredDNSNames = append(desiredDNSNames, service.DNSName)
	}
	if !dryRun {
		if err := c.config.Publisher.Prune(ctx, desiredDNSNames); err != nil {
			return nil, err
		}
	}

	selection, selectionErr := c.selectPrefix(snapshot)
	actions := c.expiredActions()
	if !dryRun {
		if err := c.retireExpired(ctx); err != nil {
			return actions, err
		}
	}

	for id, current := range c.services {
		desiredService, exists := indexed[id]
		if exists && desiredService == current.spec {
			continue
		}
		actions = append(actions, Action{Service: id, Kind: "withdraw", Detail: current.spec.DNSName})
		if !dryRun {
			if err := c.removeState(ctx, current); err != nil {
				return actions, err
			}
			delete(c.services, id)
		}
	}

	ids := make([]string, 0, len(indexed))
	for id := range indexed {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	type plan struct {
		service Service
		mode    Mode
		address netip.Addr
	}
	var plans []plan
	for _, id := range ids {
		service := indexed[id]
		current := c.services[id]
		mode, directAddress, err := c.resolveMode(ctx, service, snapshot, selection)
		if err != nil {
			if service.Mode == ModeAuto && selectionErr == nil {
				mode = ModeSplice
			} else {
				return actions, fmt.Errorf("resolve service %q: %w", id, err)
			}
		}
		if mode == ModeSplice && selectionErr != nil {
			return actions, fmt.Errorf("resolve service %q: %w", id, selectionErr)
		}

		address := directAddress
		if mode == ModeSplice {
			address, err = c.config.Deriver.Address(selection.Prefix, service.ID, 0)
			if err != nil {
				return actions, err
			}
		}
		if current != nil && current.mode == mode && ((mode == ModeDirect && current.hasAddress(address)) || (mode == ModeSplice && current.hasPrefix(selection.Prefix))) {
			continue
		}

		actions = append(actions, Action{Service: id, Kind: "prepare", Detail: fmt.Sprintf("%s address %s", mode, address)})
		if dryRun {
			if mode == ModeSplice && service.TLS {
				actions = append(actions, Action{Service: id, Kind: "certificate", Detail: "obtain or reuse a certificate for " + service.DNSName + " via dns-01"})
			}
			actions = append(actions, Action{Service: id, Kind: "publish", Detail: fmt.Sprintf("AAAA %s -> %s", service.DNSName, address)})
		}
		plans = append(plans, plan{service: service, mode: mode, address: address})
	}

	if dryRun {
		// The firewall policy is the one planned change an operator cannot
		// undo by pressing ctrl-c, so the dry run states it explicitly.
		if c.config.Firewall != nil {
			preview := hostfw.Spec{TrustedInterfaces: c.config.FirewallAllowances.TrustedInterfaces, AllowPorts: c.config.FirewallAllowances.AllowPorts}
			for _, planned := range plans {
				if planned.mode == ModeSplice {
					preview.Endpoints = append(preview.Endpoints, hostfw.Endpoint{Service: planned.service.ID, Address: planned.address, Port: planned.service.ListenPort})
				}
			}
			actions = append(actions, Action{Kind: "firewall", Detail: "drop inbound IPv6 except: " + preview.Describe()})
		}
		return actions, nil
	}

	// Warm certificates concurrently before any service is prepared: one slow
	// first issuance must not delay the other services' publication, and one
	// failing issuance must not keep the other services withdrawn.
	certificateErrors := make([]error, len(plans))
	var certificateWait sync.WaitGroup
	for index, planned := range plans {
		if planned.mode != ModeSplice || !planned.service.TLS || c.config.Certificates == nil {
			continue
		}
		certificateWait.Add(1)
		go func() {
			defer certificateWait.Done()
			_, err := c.config.Certificates.Certificate(ctx, planned.service.DNSName)
			certificateErrors[index] = err
		}()
	}
	certificateWait.Wait()

	var reconcileErrors []error
	// Open the planned sockets before anything is published. A failure here
	// is reported but does not hold the services back: another firewall may
	// already permit them, and check reports the mismatch either way.
	pending := make([]hostfw.Endpoint, 0, len(plans))
	for _, planned := range plans {
		if planned.mode == ModeSplice {
			pending = append(pending, hostfw.Endpoint{Service: planned.service.ID, Address: planned.address, Port: planned.service.ListenPort})
		}
	}
	if err := c.applyFirewall(ctx, pending); err != nil {
		reconcileErrors = append(reconcileErrors, fmt.Errorf("apply managed firewall: %w", err))
	}
	for index, planned := range plans {
		id := planned.service.ID
		if err := certificateErrors[index]; err != nil {
			reconcileErrors = append(reconcileErrors, fmt.Errorf("prepare service %q: obtain certificate for %s: %w", id, planned.service.DNSName, err))
			continue
		}
		current := c.services[id]
		if current == nil {
			current = &serviceState{spec: planned.service, mode: planned.mode}
			c.services[id] = current
		}
		newEndpoint, err := c.prepareEndpoint(ctx, planned.service, planned.mode, planned.address, selection.Prefix)
		if err != nil {
			reconcileErrors = append(reconcileErrors, fmt.Errorf("prepare service %q: %w", id, err))
			continue
		}
		addresses := append(current.addresses(), newEndpoint.address)
		addresses = uniqueAddresses(addresses)
		if err := c.config.Publisher.Ensure(ctx, dnspublish.Publication{Name: planned.service.DNSName, Addresses: addresses, EdgeAddresses: edgeAddresses(planned.service), TTL: c.config.TTL}); err != nil {
			_ = c.teardownEndpoint(context.WithoutCancel(ctx), newEndpoint)
			reconcileErrors = append(reconcileErrors, fmt.Errorf("publish service %q: %w", id, err))
			continue
		}
		for _, existing := range current.endpoints {
			if existing.address != planned.address && existing.retireAt.IsZero() {
				existing.retireAt = c.config.Now().Add(c.config.DrainGrace)
			}
		}
		current.spec = planned.service
		current.mode = planned.mode
		current.endpoints = append(current.endpoints, newEndpoint)
		actions = append(actions, Action{Service: id, Kind: "publish", Detail: fmt.Sprintf("AAAA %s -> %v", planned.service.DNSName, addresses)})
	}
	if len(reconcileErrors) > 0 {
		return actions, errors.Join(reconcileErrors...)
	}
	return actions, nil
}

// Status reads the lock-free snapshot, so it answers instantly even while a
// reconcile holds the controller lock through slow network work.
func (c *Controller) Status() []ServiceStatus {
	snapshot := c.status.Load()
	if snapshot == nil {
		return nil
	}
	statuses := make([]ServiceStatus, 0, len(*snapshot))
	for _, entry := range *snapshot {
		status := ServiceStatus{ID: entry.spec.ID, DNSName: entry.spec.DNSName, Mode: entry.mode, Backend: entry.spec.Backend, ClientIPPreserved: entry.mode == ModeDirect}
		status.EdgeAddresses = edgeAddresses(entry.spec)
		for _, endpoint := range entry.endpoints {
			status.Addresses = append(status.Addresses, endpoint.address)
			if endpoint.splicer != nil {
				spliceStatus := endpoint.splicer.Status()
				status.ActiveConnections += spliceStatus.ActiveConnections
				status.Accepted += spliceStatus.Accepted
				status.Rejected += spliceStatus.Rejected
				status.DialFailures += spliceStatus.DialFailures
				status.ClientIPPreserved = spliceStatus.ClientIPPreserved
			}
		}
		statuses = append(statuses, status)
	}
	slices.SortFunc(statuses, func(a, b ServiceStatus) int { return cmp.Compare(a.ID, b.ID) })
	return statuses
}

func (c *Controller) Sweep(ctx context.Context) ([]Action, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	defer c.refreshStatus()
	actions := c.expiredActions()
	if err := c.retireExpired(ctx); err != nil {
		return actions, err
	}
	// Retiring an endpoint removes its address, so its accept must go too.
	if len(actions) > 0 {
		if err := c.applyFirewall(ctx, nil); err != nil {
			return actions, fmt.Errorf("apply managed firewall: %w", err)
		}
	}
	return actions, nil
}

// Shutdown withdraws every publication, drains the listeners, and releases
// managed addresses. Withdraws run first so records disappear while
// connections wind down, and a slow drain can never starve them of the stop
// budget. Drains run concurrently: sequential drains would stack grace
// periods until systemd's stop timeout killed the process.
func (c *Controller) Shutdown(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	defer c.refreshStatus()
	var shutdownErrors []error
	for id, state := range c.services {
		if err := c.config.Publisher.Withdraw(ctx, state.spec.DNSName); err != nil {
			shutdownErrors = append(shutdownErrors, fmt.Errorf("withdraw service %q: %w", id, err))
		}
	}
	var wg sync.WaitGroup
	var drainMu sync.Mutex
	for id, state := range c.services {
		for _, endpoint := range state.endpoints {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := c.drainEndpoint(ctx, endpoint); err != nil {
					drainMu.Lock()
					shutdownErrors = append(shutdownErrors, fmt.Errorf("drain service %q: %w", id, err))
					drainMu.Unlock()
				}
			}()
		}
	}
	wg.Wait()
	for id, state := range c.services {
		for _, endpoint := range state.endpoints {
			if err := c.releaseEndpoint(endpoint); err != nil {
				shutdownErrors = append(shutdownErrors, fmt.Errorf("release service %q: %w", id, err))
			}
		}
		delete(c.services, id)
	}
	// Remove the managed table so a stopped daemon leaves the host exactly as
	// it was before it started. A crash leaves the table in place, which fails
	// closed; only a clean stop reverts the policy.
	if c.config.Firewall != nil && c.firewallApplied {
		if err := c.config.Firewall.Remove(ctx); err != nil {
			shutdownErrors = append(shutdownErrors, fmt.Errorf("remove managed firewall: %w", err))
		} else {
			c.firewallApplied = false
		}
	}
	return errors.Join(shutdownErrors...)
}

func (c *Controller) selectPrefix(snapshot netwatch.Snapshot) (serviceaddr.Selection, error) {
	return serviceaddr.SelectPrefix(snapshot.Candidates, c.config.PrefixOverride, c.config.Now())
}

func (c *Controller) resolveMode(ctx context.Context, service Service, snapshot netwatch.Snapshot, selection serviceaddr.Selection) (Mode, netip.Addr, error) {
	if service.Mode == ModeSplice {
		return ModeSplice, netip.Addr{}, nil
	}
	address, err := directAddress(service, snapshot, selection, c.managedAddresses(), c.config.Now())
	if err == nil {
		err = c.config.CheckDirect(ctx, netip.AddrPortFrom(address, service.ListenPort))
	}
	if err == nil {
		return ModeDirect, address, nil
	}
	if service.Mode == ModeAuto {
		return ModeSplice, netip.Addr{}, nil
	}
	return ModeDirect, netip.Addr{}, err
}

func directAddress(service Service, snapshot netwatch.Snapshot, selection serviceaddr.Selection, managed map[netip.Addr]struct{}, now time.Time) (netip.Addr, error) {
	if service.PublicAddress.IsValid() {
		for _, candidate := range snapshot.Candidates {
			if candidate.Prefix.Addr() == service.PublicAddress && eligibleCandidate(candidate, now) {
				return service.PublicAddress, nil
			}
		}
		return netip.Addr{}, fmt.Errorf("configured direct address %s is not eligible on %s", service.PublicAddress, snapshot.InterfaceName)
	}
	if !service.Backend.Addr().IsUnspecified() {
		return netip.Addr{}, errors.New("direct mode without public_address requires an unspecified IPv6 backend")
	}
	if !selection.Prefix.IsValid() {
		return netip.Addr{}, errors.New("no selected prefix for observed direct address")
	}
	var addresses []netip.Addr
	for _, candidate := range snapshot.Candidates {
		address := candidate.Prefix.Addr()
		if candidate.Prefix.Masked() == selection.Prefix && eligibleCandidate(candidate, now) {
			if _, owned := managed[address]; !owned {
				addresses = append(addresses, address)
			}
		}
	}
	if len(addresses) == 0 {
		return netip.Addr{}, errors.New("no observed stable address is eligible for direct mode")
	}
	slices.SortFunc(addresses, netip.Addr.Compare)
	return addresses[0], nil
}

func eligibleCandidate(candidate serviceaddr.Candidate, now time.Time) bool {
	address := candidate.Prefix.Addr()
	return address.Is6() && address.IsGlobalUnicast() && !address.IsPrivate() && !candidate.Temporary && !candidate.Deprecated && (candidate.PreferredUntil.IsZero() || now.Before(candidate.PreferredUntil)) && (candidate.ValidUntil.IsZero() || now.Before(candidate.ValidUntil))
}

func (c *Controller) prepareEndpoint(ctx context.Context, service Service, mode Mode, address netip.Addr, prefix netip.Prefix) (*endpoint, error) {
	result := &endpoint{address: address}
	if mode == ModeDirect {
		return result, nil
	}
	lease, err := c.config.Addresses.Ensure(ctx, prefix, service.ID)
	if err != nil {
		return nil, err
	}
	result.address = lease.Prefix.Addr()
	result.lease = &lease
	var tlsConfig *tls.Config
	if service.TLS {
		if c.config.Certificates == nil {
			_ = c.config.Addresses.Remove(lease)
			return nil, errors.New("service terminates TLS but no certificate manager is configured")
		}
		// Certificate issuance is part of prepare: the name is published only
		// after the listener can actually serve it.
		if _, err := c.config.Certificates.Certificate(ctx, service.DNSName); err != nil {
			_ = c.config.Addresses.Remove(lease)
			return nil, fmt.Errorf("obtain certificate for %s: %w", service.DNSName, err)
		}
		tlsConfig = c.config.Certificates.TLSConfig(service.DNSName)
	}
	listener, err := c.config.Listen(ctx, exposure.Config{
		ServiceID:         service.ID,
		ListenAddress:     netip.AddrPortFrom(result.address, service.ListenPort),
		BackendAddress:    service.Backend,
		MaxConnections:    service.MaxConnections,
		DialTimeout:       c.config.DialTimeout,
		IdleTimeout:       c.config.IdleTimeout,
		ProxyProtocol:     service.ProxyProtocol,
		GlobalLimiter:     c.config.GlobalLimiter,
		EdgeVerifier:      edgeVerifier(c.config.EdgeVerifier, service.Edge),
		EdgeHeaderTimeout: c.config.EdgeHeaderTimeout,
		EdgeIdentity:      service.DNSName,
		Logger:            c.config.Logger,
		TLS:               tlsConfig,
	})
	if err != nil {
		_ = c.config.Addresses.Remove(lease)
		return nil, err
	}
	result.splicer = listener
	go func() {
		if err := listener.Serve(ctx); err != nil {
			c.config.Logger.Error("service listener stopped", "service", service.ID, "address", result.address, "error", err)
		}
	}()
	return result, nil
}

func (c *Controller) retireExpired(ctx context.Context) error {
	now := c.config.Now()
	for _, state := range c.services {
		var keep []*endpoint
		var retire []*endpoint
		for _, endpoint := range state.endpoints {
			if !endpoint.retireAt.IsZero() && !now.Before(endpoint.retireAt) {
				retire = append(retire, endpoint)
			} else {
				keep = append(keep, endpoint)
			}
		}
		if len(retire) == 0 {
			continue
		}
		if err := c.config.Publisher.Ensure(ctx, dnspublish.Publication{Name: state.spec.DNSName, Addresses: endpointAddresses(keep), EdgeAddresses: edgeAddresses(state.spec), TTL: c.config.TTL}); err != nil {
			return err
		}
		for _, endpoint := range retire {
			if err := c.teardownEndpoint(ctx, endpoint); err != nil {
				return err
			}
		}
		state.endpoints = keep
	}
	return nil
}

func (c *Controller) expiredActions() []Action {
	now := c.config.Now()
	var actions []Action
	for _, state := range c.services {
		for _, endpoint := range state.endpoints {
			if !endpoint.retireAt.IsZero() && !now.Before(endpoint.retireAt) {
				actions = append(actions, Action{Service: state.spec.ID, Kind: "retire", Detail: endpoint.address.String()})
			}
		}
	}
	return actions
}

func (c *Controller) removeState(ctx context.Context, state *serviceState) error {
	if err := c.config.Publisher.Withdraw(ctx, state.spec.DNSName); err != nil {
		return err
	}
	var teardownErrors []error
	for _, endpoint := range state.endpoints {
		teardownErrors = append(teardownErrors, c.teardownEndpoint(ctx, endpoint))
	}
	return errors.Join(teardownErrors...)
}

func (c *Controller) teardownEndpoint(ctx context.Context, endpoint *endpoint) error {
	return errors.Join(c.drainEndpoint(ctx, endpoint), c.releaseEndpoint(endpoint))
}

func (c *Controller) drainEndpoint(ctx context.Context, endpoint *endpoint) error {
	if endpoint.splicer == nil {
		return nil
	}
	if active := endpoint.splicer.Status().ActiveConnections; active > 0 {
		c.config.Logger.Info("draining connections", "address", endpoint.address.String(), "active", active, "grace", c.config.DrainGrace.String())
	}
	drainContext, cancel := context.WithTimeout(ctx, c.config.DrainGrace)
	defer cancel()
	err := endpoint.splicer.Shutdown(drainContext)
	if err != nil && errors.Is(err, context.DeadlineExceeded) {
		// Connections held past the grace period were force closed. That is
		// the drain policy completing, not a failure.
		c.config.Logger.Warn("force closed connections after drain grace", "address", endpoint.address.String())
		return nil
	}
	return err
}

func (c *Controller) releaseEndpoint(endpoint *endpoint) error {
	if endpoint.lease == nil {
		return nil
	}
	return c.config.Addresses.Remove(*endpoint.lease)
}

func (c *Controller) managedAddresses() map[netip.Addr]struct{} {
	addresses := make(map[netip.Addr]struct{})
	for _, state := range c.services {
		for _, endpoint := range state.endpoints {
			if endpoint.lease != nil {
				addresses[endpoint.address] = struct{}{}
			}
		}
	}
	return addresses
}

func (s *serviceState) hasAddress(address netip.Addr) bool {
	for _, endpoint := range s.endpoints {
		if endpoint.address == address {
			return true
		}
	}
	return false
}

func (s *serviceState) hasPrefix(prefix netip.Prefix) bool {
	for _, endpoint := range s.endpoints {
		if endpoint.lease != nil && endpoint.lease.Prefix.Masked() == prefix {
			return true
		}
	}
	return false
}

func (s *serviceState) addresses() []netip.Addr {
	return endpointAddresses(s.endpoints)
}

func endpointAddresses(endpoints []*endpoint) []netip.Addr {
	addresses := make([]netip.Addr, 0, len(endpoints))
	for _, endpoint := range endpoints {
		addresses = append(addresses, endpoint.address)
	}
	return addresses
}

func uniqueAddresses(addresses []netip.Addr) []netip.Addr {
	slices.SortFunc(addresses, netip.Addr.Compare)
	return slices.Compact(addresses)
}

func edgeAddresses(service Service) []netip.Addr {
	if service.Edge {
		return []netip.Addr{service.EdgeAddress}
	}
	return nil
}

func edgeVerifier(verifier *edgeauth.Verifier, enabled bool) *edgeauth.Verifier {
	if enabled {
		return verifier
	}
	return nil
}
