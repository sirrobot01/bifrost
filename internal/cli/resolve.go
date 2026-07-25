package cli

import (
	"fmt"
	"net/netip"
	"slices"
	"time"

	"github.com/sirrobot01/bifrost/internal/config"
	"github.com/sirrobot01/bifrost/internal/netwatch"
	"github.com/sirrobot01/bifrost/internal/serviceaddr"
)

type resolvedService struct {
	Name              string         `json:"name"`
	DNSName           string         `json:"dns"`
	Mode              string         `json:"mode"`
	Address           netip.Addr     `json:"address"`
	Listen            uint16         `json:"listen"`
	Backend           netip.AddrPort `json:"backend"`
	ClientIPPreserved bool           `json:"client_ip_preserved"`
}

type resolution struct {
	Interface       string            `json:"interface"`
	MTU             int               `json:"mtu"`
	SelectedPrefix  netip.Prefix      `json:"selected_prefix,omitempty"`
	IgnoredPrefixes []netip.Prefix    `json:"ignored_prefixes,omitempty"`
	Services        []resolvedService `json:"services"`
}

func resolve(configFile config.Config) (resolution, error) {
	needPrefix := false
	needSecret := false
	for _, service := range configFile.StaticServices {
		mode := selectedMode(service)
		if mode == "splice" {
			needPrefix = true
			needSecret = true
		}
		if mode == "direct" && service.PublicAddress == "" {
			needPrefix = true
		}
	}

	result := resolution{Interface: configFile.Interface}
	var selection serviceaddr.Selection
	var deriver serviceaddr.Deriver
	var snapshot netwatch.Snapshot
	if needPrefix {
		var err error
		snapshot, err = platformSnapshot(configFile.Interface)
		if err != nil {
			return resolution{}, err
		}
		result.MTU = snapshot.MTU
		var override netip.Prefix
		if configFile.PrefixOverride != "" {
			override = netip.MustParsePrefix(configFile.PrefixOverride)
		}
		selection, err = serviceaddr.SelectPrefix(snapshot.Candidates, override, time.Now())
		if err != nil {
			return resolution{}, err
		}
		result.SelectedPrefix = selection.Prefix
		for _, candidate := range selection.Candidates {
			if candidate != selection.Prefix {
				result.IgnoredPrefixes = append(result.IgnoredPrefixes, candidate)
			}
		}
		if needSecret {
			secret, err := config.ReadSecret(configFile.SecretFile)
			if err != nil {
				return resolution{}, err
			}
			deriver, err = serviceaddr.NewDeriver(secret)
			if err != nil {
				return resolution{}, err
			}
		}
	} else if snapshot, err := platformSnapshot(configFile.Interface); err == nil {
		result.MTU = snapshot.MTU
	}

	result.Services = make([]resolvedService, 0, len(configFile.StaticServices))
	for _, service := range configFile.StaticServices {
		var err error
		backend := netip.MustParseAddrPort(service.Backend)
		mode := selectedMode(service)
		var address netip.Addr
		if mode == "splice" {
			address, err = deriver.Address(selection.Prefix, service.Name, 0)
			if err != nil {
				return resolution{}, fmt.Errorf("derive address for %s: %w", service.Name, err)
			}
		} else if service.PublicAddress != "" {
			address = netip.MustParseAddr(service.PublicAddress)
		} else {
			address, err = observedAddress(snapshot, selection, time.Now())
			if err != nil {
				return resolution{}, fmt.Errorf("select direct address for %s: %w", service.Name, err)
			}
		}
		result.Services = append(result.Services, resolvedService{
			Name:              service.Name,
			DNSName:           service.DNSName,
			Mode:              mode,
			Address:           address,
			Listen:            service.Listen,
			Backend:           backend,
			ClientIPPreserved: mode == "direct" || service.ProxyProtocol,
		})
	}
	return result, nil
}

func selectedMode(service config.StaticService) string {
	if service.Mode != "auto" {
		return service.Mode
	}
	backend := netip.MustParseAddrPort(service.Backend)
	if service.PublicAddress != "" && backend.Addr().Is6() && backend.Port() == service.Listen {
		if publicAddress, err := netip.ParseAddr(service.PublicAddress); err == nil && backend.Addr() == publicAddress {
			return "direct"
		}
	}
	if backend.Addr().Is6() && backend.Addr().IsUnspecified() && backend.Port() == service.Listen {
		return "direct"
	}
	return "splice"
}

func observedAddress(snapshot netwatch.Snapshot, selection serviceaddr.Selection, now time.Time) (netip.Addr, error) {
	var addresses []netip.Addr
	for _, candidate := range snapshot.Candidates {
		if candidate.Prefix.Masked() != selection.Prefix || candidate.Temporary || candidate.Deprecated {
			continue
		}
		if !candidate.PreferredUntil.IsZero() && !now.Before(candidate.PreferredUntil) {
			continue
		}
		if !candidate.ValidUntil.IsZero() && !now.Before(candidate.ValidUntil) {
			continue
		}
		addresses = append(addresses, candidate.Prefix.Addr())
	}
	if len(addresses) == 0 {
		return netip.Addr{}, fmt.Errorf("no stable address exists in %s", selection.Prefix)
	}
	slices.SortFunc(addresses, netip.Addr.Compare)
	return addresses[0], nil
}
