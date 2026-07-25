//go:build linux

package serviceaddr

import (
	"net/netip"
	"testing"
)

func TestNetlinkAddress(t *testing.T) {
	t.Parallel()

	prefix := netip.MustParsePrefix("2001:db8:1::42/64")
	address, err := netlinkAddress(prefix)
	if err != nil {
		t.Fatal(err)
	}
	if got := address.IP.String(); got != prefix.Addr().String() {
		t.Fatalf("address IP = %s, want %s", got, prefix.Addr())
	}
	ones, bits := address.Mask.Size()
	if ones != 64 || bits != 128 {
		t.Fatalf("address mask = %d/%d", ones, bits)
	}
}
