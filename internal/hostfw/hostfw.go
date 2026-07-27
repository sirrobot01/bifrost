// Package hostfw manages the host firewall rules that scope inbound IPv6 to
// the published services.
//
// Bifrost owns exactly one nftables table and never touches another. That is
// enough to be authoritative: with several base chains on the same hook, a
// drop in any one of them drops the packet, so a Bifrost chain with a drop
// policy narrows a permissive host or router even when another table accepts.
// The reverse does not hold, which is why advisory mode still reports another
// table's drop policy rather than trying to override it.
package hostfw

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
)

// TableName is the nftables table Bifrost owns. Anything outside it is left
// alone.
const TableName = "bifrost"

// Endpoint is one published socket that must accept inbound connections.
type Endpoint struct {
	Service string
	Address netip.Addr
	Port    uint16
}

// Spec is the complete desired inbound policy. Applying a spec replaces the
// managed table wholesale, so a spec is always the full intent, never a delta.
type Spec struct {
	Endpoints []Endpoint
	// TrustedInterfaces accept everything, for links that carry their own
	// authentication such as a VPN.
	TrustedInterfaces []string
	// AllowPorts are TCP ports accepted on any address, for services Bifrost
	// does not publish but the operator still needs reachable, such as SSH.
	AllowPorts []uint16
}

// Manager applies and removes the managed table.
type Manager interface {
	Apply(ctx context.Context, spec Spec) error
	// Remove deletes the managed table, restoring whatever policy applied
	// before Bifrost started.
	Remove(ctx context.Context) error
}

// ErrUnsupported is returned by New on platforms without nftables.
var ErrUnsupported = errors.New("managed firewall mode requires Linux with nftables")

// Describe renders a spec for logs and dry-run plans.
func (s Spec) Describe() string {
	parts := make([]string, 0, len(s.Endpoints)+2)
	for _, endpoint := range s.Endpoints {
		parts = append(parts, fmt.Sprintf("%s->[%s]:%d", endpoint.Service, endpoint.Address, endpoint.Port))
	}
	if len(s.AllowPorts) > 0 {
		ports := make([]string, 0, len(s.AllowPorts))
		for _, port := range s.AllowPorts {
			ports = append(ports, fmt.Sprint(port))
		}
		parts = append(parts, "allow tcp "+strings.Join(ports, ","))
	}
	if len(s.TrustedInterfaces) > 0 {
		parts = append(parts, "trusted "+strings.Join(s.TrustedInterfaces, ","))
	}
	if len(parts) == 0 {
		return "no inbound accepts"
	}
	return strings.Join(parts, ", ")
}

// normalize sorts and deduplicates a spec so that an unchanged intent produces
// an identical spec, letting callers skip redundant applies.
func (s Spec) normalize() Spec {
	endpoints := slices.Clone(s.Endpoints)
	slices.SortFunc(endpoints, func(a, b Endpoint) int {
		if c := a.Address.Compare(b.Address); c != 0 {
			return c
		}
		return int(a.Port) - int(b.Port)
	})
	endpoints = slices.CompactFunc(endpoints, func(a, b Endpoint) bool {
		return a.Address == b.Address && a.Port == b.Port
	})
	interfaces := slices.Clone(s.TrustedInterfaces)
	slices.Sort(interfaces)
	interfaces = slices.Compact(interfaces)
	ports := slices.Clone(s.AllowPorts)
	slices.Sort(ports)
	ports = slices.Compact(ports)
	return Spec{Endpoints: endpoints, TrustedInterfaces: interfaces, AllowPorts: ports}
}

// Equal reports whether two specs express the same policy. Endpoints compare
// by address and port only: the service name is a label for logs and never
// reaches a rule, so renaming a service must not rewrite the firewall.
func (s Spec) Equal(other Spec) bool {
	left, right := s.normalize(), other.normalize()
	sameEndpoint := func(a, b Endpoint) bool { return a.Address == b.Address && a.Port == b.Port }
	return slices.EqualFunc(left.Endpoints, right.Endpoints, sameEndpoint) &&
		slices.Equal(left.TrustedInterfaces, right.TrustedInterfaces) &&
		slices.Equal(left.AllowPorts, right.AllowPorts)
}
