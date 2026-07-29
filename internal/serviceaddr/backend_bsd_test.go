//go:build darwin || freebsd || openbsd

package serviceaddr

import (
	"net/netip"
	"reflect"
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
	var got []string
	backend := &ifconfigBackend{interfaceName: "test0"}
	backend.run = func(arguments ...string) ([]byte, error) {
		got = append([]string(nil), arguments...)
		if len(arguments) == 2 { // Status inspection.
			return nil, nil
		}
		return nil, nil
	}
	if err := backend.Ensure(netip.MustParsePrefix("2001:db8::42/64")); err != nil {
		t.Fatal(err)
	}
	if len(got) < 4 || !reflect.DeepEqual(got[:2], []string{"test0", "inet6"}) {
		t.Fatalf("ifconfig arguments = %v", got)
	}
}
