package hostfw

import (
	"net/netip"
	"strings"
	"testing"
)

func TestSpecEqualIgnoresOrder(t *testing.T) {
	t.Parallel()

	left := Spec{
		Endpoints: []Endpoint{
			{Service: "plex", Address: netip.MustParseAddr("2001:db8::2"), Port: 443},
			{Service: "emby", Address: netip.MustParseAddr("2001:db8::1"), Port: 443},
		},
		TrustedInterfaces: []string{"tailscale0", "lo"},
		AllowPorts:        []uint16{22, 2222},
	}
	right := Spec{
		Endpoints: []Endpoint{
			{Service: "emby", Address: netip.MustParseAddr("2001:db8::1"), Port: 443},
			{Service: "plex", Address: netip.MustParseAddr("2001:db8::2"), Port: 443},
		},
		TrustedInterfaces: []string{"lo", "tailscale0"},
		AllowPorts:        []uint16{2222, 22},
	}
	if !left.Equal(right) {
		t.Fatal("specs differing only in order are not equal")
	}

	changed := Spec{Endpoints: append([]Endpoint(nil), left.Endpoints...), TrustedInterfaces: left.TrustedInterfaces, AllowPorts: left.AllowPorts}
	changed.Endpoints[0].Port = 8096
	if left.Equal(changed) {
		t.Fatal("a changed port compared equal")
	}
}

func TestSpecEqualIgnoresServiceRename(t *testing.T) {
	t.Parallel()

	// The rule set is addresses and ports; the service name is only a label
	// for logs, so renaming must not force a firewall rewrite.
	address := netip.MustParseAddr("2001:db8::1")
	left := Spec{Endpoints: []Endpoint{{Service: "media", Address: address, Port: 443}}}
	right := Spec{Endpoints: []Endpoint{{Service: "renamed", Address: address, Port: 443}}}
	if !left.Equal(right) {
		t.Fatal("a service rename forced a firewall rewrite")
	}
}

func TestSpecDescribe(t *testing.T) {
	t.Parallel()

	spec := Spec{
		Endpoints:         []Endpoint{{Service: "emby", Address: netip.MustParseAddr("2001:db8::1"), Port: 443}},
		TrustedInterfaces: []string{"tailscale0"},
		AllowPorts:        []uint16{22},
	}
	described := spec.Describe()
	for _, want := range []string{"emby->[2001:db8::1]:443", "allow tcp 22", "trusted tailscale0"} {
		if !strings.Contains(described, want) {
			t.Fatalf("describe = %q, missing %q", described, want)
		}
	}
	if empty := (Spec{}).Describe(); empty != "no inbound accepts" {
		t.Fatalf("empty describe = %q", empty)
	}
}
