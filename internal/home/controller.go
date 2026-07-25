package home

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"slices"
	"sync"
	"time"

	"github.com/sirrobot01/bifrost/internal/dnspublish"
	"github.com/sirrobot01/bifrost/internal/exposure"
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
	Backend           netip.AddrPort `json:"backend"`
	ClientIPPreserved bool           `json:"client_ip_preserved"`
	ActiveConnections int64          `json:"active_connections"`
}

type ControllerConfig struct {
	Addresses      AddressManager
	Deriver        serviceaddr.Deriver
	Publisher      Publisher
	Listen         ListenerFactory
	TTL            time.Duration
	DrainGrace     time.Duration
	DialTimeout    time.Duration
	IdleTimeout    time.Duration
	Logger         *slog.Logger
	Now            func() time.Time
	CheckDirect    func(context.Context, netip.AddrPort) error
	PrefixOverride netip.Prefix
}

type Controller struct {
	config   ControllerConfig
	mu       sync.RWMutex
	services map[string]*serviceState
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
	return &Controller{config: config, services: make(map[string]*serviceState)}, nil
}

func (c *Controller) Reconcile(ctx context.Context, desired []Service, snapshot netwatch.Snapshot, dryRun bool) ([]Action, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	indexed := make(map[string]Service, len(desired))
	desiredDNSNames := make([]string, 0, len(desired))
	for _, service := range desired {
		if err := service.validate(); err != nil {
			return nil, fmt.Errorf("service %q: %w", service.ID, err)
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
			actions = append(actions, Action{Service: id, Kind: "publish", Detail: fmt.Sprintf("AAAA %s -> %s", service.DNSName, address)})
			continue
		}

		if current == nil {
			current = &serviceState{spec: service, mode: mode}
			c.services[id] = current
		}
		newEndpoint, err := c.prepareEndpoint(ctx, service, mode, address, selection.Prefix)
		if err != nil {
			return actions, fmt.Errorf("prepare service %q: %w", id, err)
		}
		addresses := append(current.addresses(), newEndpoint.address)
		addresses = uniqueAddresses(addresses)
		if err := c.config.Publisher.Ensure(ctx, dnspublish.Publication{Name: service.DNSName, Addresses: addresses, TTL: c.config.TTL}); err != nil {
			_ = c.teardownEndpoint(context.WithoutCancel(ctx), newEndpoint)
			return actions, fmt.Errorf("publish service %q: %w", id, err)
		}
		for _, existing := range current.endpoints {
			if existing.address != address && existing.retireAt.IsZero() {
				existing.retireAt = c.config.Now().Add(c.config.DrainGrace)
			}
		}
		current.spec = service
		current.mode = mode
		current.endpoints = append(current.endpoints, newEndpoint)
		actions = append(actions, Action{Service: id, Kind: "publish", Detail: fmt.Sprintf("AAAA %s -> %v", service.DNSName, addresses)})
	}
	return actions, nil
}

func (c *Controller) Status() []ServiceStatus {
	c.mu.RLock()
	defer c.mu.RUnlock()
	statuses := make([]ServiceStatus, 0, len(c.services))
	for _, state := range c.services {
		status := ServiceStatus{ID: state.spec.ID, DNSName: state.spec.DNSName, Mode: state.mode, Backend: state.spec.Backend, ClientIPPreserved: state.mode == ModeDirect}
		for _, endpoint := range state.endpoints {
			status.Addresses = append(status.Addresses, endpoint.address)
			if endpoint.splicer != nil {
				status.ActiveConnections += endpoint.splicer.Status().ActiveConnections
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
	actions := c.expiredActions()
	if err := c.retireExpired(ctx); err != nil {
		return actions, err
	}
	return actions, nil
}

func (c *Controller) Shutdown(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	var shutdownErrors []error
	for id, state := range c.services {
		if err := c.removeState(ctx, state); err != nil {
			shutdownErrors = append(shutdownErrors, fmt.Errorf("remove service %q: %w", id, err))
		}
		delete(c.services, id)
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
	listener, err := c.config.Listen(ctx, exposure.Config{
		ServiceID:      service.ID,
		ListenAddress:  netip.AddrPortFrom(result.address, service.ListenPort),
		BackendAddress: service.Backend,
		MaxConnections: service.MaxConnections,
		DialTimeout:    c.config.DialTimeout,
		IdleTimeout:    c.config.IdleTimeout,
		Logger:         c.config.Logger,
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
		if err := c.config.Publisher.Ensure(ctx, dnspublish.Publication{Name: state.spec.DNSName, Addresses: endpointAddresses(keep), TTL: c.config.TTL}); err != nil {
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
	var teardownErrors []error
	if endpoint.splicer != nil {
		drainContext, cancel := context.WithTimeout(ctx, c.config.DrainGrace)
		teardownErrors = append(teardownErrors, endpoint.splicer.Shutdown(drainContext))
		cancel()
	}
	if endpoint.lease != nil {
		teardownErrors = append(teardownErrors, c.config.Addresses.Remove(*endpoint.lease))
	}
	return errors.Join(teardownErrors...)
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
