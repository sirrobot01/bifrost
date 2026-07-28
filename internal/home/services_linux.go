//go:build linux

package home

import (
	"fmt"
	"net/netip"

	"github.com/sirrobot01/bifrost/internal/config"
)

func servicesFromConfig(configFile config.Config) ([]Service, error) {
	services := make([]Service, 0, len(configFile.StaticServices))
	for _, configured := range configFile.StaticServices {
		service := Service{
			ID:             configured.Name,
			DNSName:        configured.DNSName,
			Mode:           Mode(configured.Mode),
			ListenPort:     configured.Listen,
			Backend:        netip.MustParseAddrPort(configured.Backend),
			ProxyProtocol:  configured.ProxyProtocol,
			Edge:           configured.Edge,
			MaxConnections: 1024,
			TLS:            configured.TerminatesTLS(),
		}
		if configured.PublicAddress != "" {
			service.PublicAddress = netip.MustParseAddr(configured.PublicAddress)
		}
		if configured.Edge {
			for _, address := range configFile.Edge.IPv4Addresses {
				service.EdgeAddresses = append(service.EdgeAddresses, netip.MustParseAddr(address))
			}
		}
		if err := service.validate(); err != nil {
			return nil, fmt.Errorf("service %q: %w", service.ID, err)
		}
		services = append(services, service)
	}
	return services, nil
}
