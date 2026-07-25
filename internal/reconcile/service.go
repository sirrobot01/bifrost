package reconcile

import (
	"errors"
	"net/netip"
)

// Mode controls whether Bifrost participates in a service's data path.
type Mode string

const (
	ModeDirect Mode = "direct"
	ModeSplice Mode = "splice"
)

// Service is the complete desired publication state for one service.
type Service struct {
	ID            string
	DNSName       string
	Mode          Mode
	PublicAddress netip.Addr
	ListenPort    uint16
	Backend       netip.AddrPort
}

// Validate checks whether a service can be reconciled.
func (s Service) Validate() error {
	if s.ID == "" {
		return errors.New("service ID is required")
	}
	if s.DNSName == "" {
		return errors.New("service DNS name is required")
	}
	if s.Mode != ModeDirect && s.Mode != ModeSplice {
		return errors.New("service mode must be direct or splice")
	}
	if !s.PublicAddress.IsValid() || !s.PublicAddress.Is6() || s.PublicAddress.Is4In6() || !s.PublicAddress.IsGlobalUnicast() || s.PublicAddress.IsPrivate() {
		return errors.New("service public address must be a public IPv6 address")
	}
	if s.ListenPort == 0 {
		return errors.New("service listen port is required")
	}
	if !s.Backend.IsValid() {
		return errors.New("service backend is required")
	}
	if s.Mode == ModeDirect && (s.Backend.Addr() != s.PublicAddress || s.Backend.Port() != s.ListenPort) {
		return errors.New("direct service backend must match its public address and port")
	}
	return nil
}
