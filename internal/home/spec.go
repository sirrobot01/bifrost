package home

import (
	"fmt"
	"net/netip"
	"slices"

	"github.com/sirrobot01/bifrost/internal/config"
)

type Mode string

const (
	ModeAuto   Mode = "auto"
	ModeDirect Mode = "direct"
	ModeSplice Mode = "splice"
)

type Service struct {
	ID             string
	DNSName        string
	Mode           Mode
	PublicAddress  netip.Addr
	ListenPort     uint16
	Backend        netip.AddrPort
	ProxyProtocol  bool
	Edge           bool
	EdgeAddresses  []netip.Addr
	MaxConnections int
	// TLS asks Bifrost to terminate TLS on the splice listener with an
	// automatically issued certificate. Direct mode ignores it.
	TLS bool
}

// Equal compares two service definitions. A slice field rules out the
// language's own comparison, and reconcile needs to know whether a service
// changed.
func (s Service) Equal(other Service) bool {
	return slices.Equal(s.EdgeAddresses, other.EdgeAddresses) &&
		s.ID == other.ID &&
		s.DNSName == other.DNSName &&
		s.Mode == other.Mode &&
		s.PublicAddress == other.PublicAddress &&
		s.ListenPort == other.ListenPort &&
		s.Backend == other.Backend &&
		s.ProxyProtocol == other.ProxyProtocol &&
		s.Edge == other.Edge &&
		s.MaxConnections == other.MaxConnections &&
		s.TLS == other.TLS
}

func (s Service) validate() error {
	configured := config.StaticService{
		Name:          s.ID,
		DNSName:       s.DNSName,
		Mode:          string(s.Mode),
		Listen:        s.ListenPort,
		Backend:       s.Backend.String(),
		ProxyProtocol: s.ProxyProtocol,
		Edge:          s.Edge,
	}
	if s.PublicAddress.IsValid() {
		configured.PublicAddress = s.PublicAddress.String()
	}
	if err := configured.Validate(); err != nil {
		return err
	}
	if s.Edge {
		if len(s.EdgeAddresses) == 0 {
			return fmt.Errorf("an edge service needs at least one edge address")
		}
		for _, address := range s.EdgeAddresses {
			if !address.IsValid() || !address.Is4() || !address.IsGlobalUnicast() || address.IsPrivate() {
				return fmt.Errorf("edge address %s must be public IPv4", address)
			}
		}
	}
	if s.MaxConnections <= 0 {
		return fmt.Errorf("maximum connections must be positive")
	}
	return nil
}
