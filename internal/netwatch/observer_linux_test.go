//go:build linux

package netwatch

import (
	"net"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

func TestCandidateFromNetlink(t *testing.T) {
	t.Parallel()

	observedAt := time.Date(2026, time.July, 25, 12, 0, 0, 0, time.UTC)
	address := netlink.Addr{
		IPNet: &net.IPNet{
			IP:   net.ParseIP("2001:db8:1::42"),
			Mask: net.CIDRMask(64, 128),
		},
		Scope:       int(netlink.SCOPE_UNIVERSE),
		PreferedLft: 300,
		ValidLft:    600,
	}

	candidate, ok := candidateFromNetlink(address, observedAt)
	if !ok {
		t.Fatal("netlink address was rejected")
	}
	if candidate.Prefix.String() != "2001:db8:1::42/64" {
		t.Fatalf("prefix = %s", candidate.Prefix)
	}
	if candidate.PreferredUntil != observedAt.Add(300*time.Second) {
		t.Fatalf("preferred deadline = %s", candidate.PreferredUntil)
	}
}

func TestCandidateFromNetlinkRejectsDADFailure(t *testing.T) {
	t.Parallel()

	address := netlink.Addr{
		IPNet: &net.IPNet{
			IP:   net.ParseIP("2001:db8:1::42"),
			Mask: net.CIDRMask(64, 128),
		},
		Flags:       unix.IFA_F_DADFAILED,
		Scope:       int(netlink.SCOPE_UNIVERSE),
		PreferedLft: 300,
		ValidLft:    600,
	}

	if _, ok := candidateFromNetlink(address, time.Now()); ok {
		t.Fatal("DAD-failed address produced a candidate")
	}
}
