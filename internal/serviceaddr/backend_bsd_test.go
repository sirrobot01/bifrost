//go:build darwin || freebsd || openbsd

package serviceaddr

import (
	"net/netip"
	"slices"
	"testing"
)

func TestIfconfigBackendStatusReadsDADState(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		line   string
		status AddressStatus
	}{
		{name: "ready", line: "inet6 2001:db8::42 prefixlen 64", status: AddressReady},
		{name: "tentative", line: "inet6 2001:db8::42 prefixlen 64 tentative", status: AddressTentative},
		{name: "duplicate", line: "inet6 2001:db8::42 prefixlen 64 duplicated", status: AddressDADFailed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			backend := &ifconfigBackend{interfaceName: "test0", run: func(...string) ([]byte, error) { return []byte(test.line), nil }}
			got, err := backend.Status(netip.MustParseAddr("2001:db8::42"))
			if err != nil || got != test.status {
				t.Fatalf("Status = %v, %v; want %v", got, err, test.status)
			}
		})
	}
}

func TestIfconfigBackendUsesPlatformArguments(t *testing.T) {
	t.Parallel()
	prefix := netip.MustParsePrefix("2001:db8::42/64")
	tests := []struct {
		name   string
		style  IfconfigStyle
		add    []string
		remove []string
	}{
		{name: "macOS", style: IfconfigDarwin, add: []string{"test0", "inet6", "2001:db8::42", "prefixlen", "64", "alias"}, remove: []string{"test0", "inet6", "2001:db8::42", "-alias"}},
		{name: "FreeBSD", style: IfconfigFreeBSD, add: []string{"test0", "inet6", "2001:db8::42", "prefixlen", "64", "alias"}, remove: []string{"test0", "inet6", "2001:db8::42/64", "delete"}},
		{name: "OpenBSD", style: IfconfigOpenBSD, add: []string{"test0", "inet6", "2001:db8::42", "prefixlen", "64", "alias"}, remove: []string{"test0", "inet6", "2001:db8::42", "delete"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			backend := &ifconfigBackend{interfaceName: "test0", style: test.style}
			if got := backend.addArguments(prefix); !slices.Equal(got, test.add) {
				t.Fatalf("add arguments = %v, want %v", got, test.add)
			}
			if got := backend.removeArguments(prefix); !slices.Equal(got, test.remove) {
				t.Fatalf("remove arguments = %v, want %v", got, test.remove)
			}
		})
	}
}
