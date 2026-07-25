package serviceaddr

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"time"
)

const (
	defaultDADAttempts  = 3
	defaultPollInterval = 50 * time.Millisecond
)

// AddressStatus is the kernel state of a managed address.
type AddressStatus uint8

const (
	AddressAbsent AddressStatus = iota
	AddressTentative
	AddressReady
	AddressDADFailed
)

// AddressBackend applies and inspects addresses on one network interface.
type AddressBackend interface {
	Ensure(netip.Prefix) error
	Remove(netip.Prefix) error
	Status(netip.Addr) (AddressStatus, error)
}

// Lease identifies an address created or adopted for one service.
type Lease struct {
	Prefix     netip.Prefix
	DADCounter uint32
}

// Manager owns the DAD-aware lifecycle of deterministic service addresses.
type Manager struct {
	backend      AddressBackend
	deriver      Deriver
	dadAttempts  uint32
	pollInterval time.Duration
}

// NewManager returns a service-address lifecycle manager.
func NewManager(backend AddressBackend, deriver Deriver) (*Manager, error) {
	if backend == nil {
		return nil, errors.New("address backend is required")
	}
	if len(deriver.secret) < minimumSecretSize {
		return nil, errors.New("service address deriver is not initialized")
	}

	return &Manager{
		backend:      backend,
		deriver:      deriver,
		dadAttempts:  defaultDADAttempts,
		pollInterval: defaultPollInterval,
	}, nil
}

// Ensure creates or adopts a ready service address.
func (m *Manager) Ensure(ctx context.Context, prefix netip.Prefix, serviceID string) (Lease, error) {
	if err := ctx.Err(); err != nil {
		return Lease{}, err
	}

	ticker := time.NewTicker(m.pollInterval)
	defer ticker.Stop()

	for dadCounter := uint32(0); dadCounter < m.dadAttempts; dadCounter++ {
		address, err := m.deriver.Address(prefix, serviceID, dadCounter)
		if err != nil {
			return Lease{}, err
		}
		lease := Lease{
			Prefix:     netip.PrefixFrom(address, prefix.Bits()),
			DADCounter: dadCounter,
		}

		if err := m.backend.Ensure(lease.Prefix); err != nil {
			return Lease{}, fmt.Errorf("ensure service address %s: %w", address, err)
		}

		for {
			status, err := m.backend.Status(address)
			if err != nil {
				return Lease{}, fmt.Errorf("inspect service address %s: %w", address, err)
			}
			switch status {
			case AddressReady:
				return lease, nil
			case AddressDADFailed:
				if err := m.backend.Remove(lease.Prefix); err != nil {
					return Lease{}, fmt.Errorf("remove duplicate service address %s: %w", address, err)
				}
				goto retry
			case AddressAbsent, AddressTentative:
			}

			select {
			case <-ctx.Done():
				if err := m.backend.Remove(lease.Prefix); err != nil {
					return Lease{}, errors.Join(ctx.Err(), fmt.Errorf("remove pending service address %s: %w", address, err))
				}
				return Lease{}, ctx.Err()
			case <-ticker.C:
			}
		}

	retry:
	}

	return Lease{}, fmt.Errorf("service address for %q failed DAD after %d attempts", serviceID, m.dadAttempts)
}

// Remove releases a service-address lease.
func (m *Manager) Remove(lease Lease) error {
	if !lease.Prefix.IsValid() || !lease.Prefix.Addr().Is6() || lease.Prefix.Bits() != 64 {
		return errors.New("lease must contain an IPv6 /64 service address")
	}
	if err := m.backend.Remove(lease.Prefix); err != nil {
		return fmt.Errorf("remove service address %s: %w", lease.Prefix.Addr(), err)
	}
	return nil
}
