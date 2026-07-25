package home

import (
	"fmt"
	"net/netip"

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
	MaxConnections int
}

func (s Service) validate() error {
	configured := config.StaticService{
		Name:          s.ID,
		DNSName:       s.DNSName,
		Mode:          string(s.Mode),
		Listen:        s.ListenPort,
		Backend:       s.Backend.String(),
		ProxyProtocol: s.ProxyProtocol,
	}
	if s.PublicAddress.IsValid() {
		configured.PublicAddress = s.PublicAddress.String()
	}
	if err := configured.Validate(); err != nil {
		return err
	}
	if s.MaxConnections <= 0 {
		return fmt.Errorf("maximum connections must be positive")
	}
	return nil
}
