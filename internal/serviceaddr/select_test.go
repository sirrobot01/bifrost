package serviceaddr

import (
	"net/netip"
	"slices"
	"testing"
	"time"
)

func TestSelectPrefix(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 25, 12, 0, 0, 0, time.UTC)
	candidates := []Candidate{
		{Prefix: netip.MustParsePrefix("2001:db8:2::cafe/64")},
		{Prefix: netip.MustParsePrefix("2001:db8:1::beef/64")},
		{Prefix: netip.MustParsePrefix("2001:db8:1::feed/64")},
		{Prefix: netip.MustParsePrefix("2001:db8:3::1/64"), Temporary: true},
		{Prefix: netip.MustParsePrefix("2001:db8:4::1/64"), Deprecated: true},
		{Prefix: netip.MustParsePrefix("fd00::1/64")},
		{Prefix: netip.MustParsePrefix("2001:db8:5::1/64"), PreferredUntil: now},
		{Prefix: netip.MustParsePrefix("2001:db8:6::1/64"), ValidUntil: now.Add(-time.Second)},
	}

	selection, err := SelectPrefix(candidates, netip.Prefix{}, now)
	if err != nil {
		t.Fatal(err)
	}

	want := []netip.Prefix{
		netip.MustParsePrefix("2001:db8:1::/64"),
		netip.MustParsePrefix("2001:db8:2::/64"),
	}
	if !slices.Equal(selection.Candidates, want) {
		t.Fatalf("eligible prefixes = %v, want %v", selection.Candidates, want)
	}
	if selection.Prefix != want[0] {
		t.Fatalf("selected prefix = %s, want %s", selection.Prefix, want[0])
	}
}

func TestSelectPrefixOverride(t *testing.T) {
	t.Parallel()

	candidates := []Candidate{
		{Prefix: netip.MustParsePrefix("2001:db8:1::1/64")},
		{Prefix: netip.MustParsePrefix("2001:db8:2::1/64")},
	}
	override := netip.MustParsePrefix("2001:db8:2::/64")

	selection, err := SelectPrefix(candidates, override, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if selection.Prefix != override {
		t.Fatalf("selected prefix = %s, want %s", selection.Prefix, override)
	}
}

func TestSelectPrefixRejectsUnavailableOverride(t *testing.T) {
	t.Parallel()

	candidates := []Candidate{{Prefix: netip.MustParsePrefix("2001:db8:1::1/64")}}
	override := netip.MustParsePrefix("2001:db8:2::/64")

	if _, err := SelectPrefix(candidates, override, time.Time{}); err == nil {
		t.Fatal("SelectPrefix succeeded with an unavailable override")
	}
}

func TestSelectPrefixRejectsEmptyCandidateSet(t *testing.T) {
	t.Parallel()

	candidates := []Candidate{
		{Prefix: netip.MustParsePrefix("fe80::1/64")},
		{Prefix: netip.MustParsePrefix("2001:db8:1::1/64"), Temporary: true},
	}

	if _, err := SelectPrefix(candidates, netip.Prefix{}, time.Time{}); err == nil {
		t.Fatal("SelectPrefix succeeded without an eligible prefix")
	}
}
