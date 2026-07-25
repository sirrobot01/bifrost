package edge

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"
)

type fakeResolver struct {
	addresses []netip.Addr
	ttl       time.Duration
	err       error
	calls     int
}

func (r *fakeResolver) Lookup(context.Context, string) ([]netip.Addr, time.Duration, error) {
	r.calls++
	return r.addresses, r.ttl, r.err
}

func TestDNSCacheUsesTTLAndStaleOnError(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 25, 12, 0, 0, 0, time.UTC)
	resolver := &fakeResolver{addresses: []netip.Addr{netip.MustParseAddr("2001:4860::10")}, ttl: time.Minute}
	cache, err := NewDNSCache(resolver, 10*time.Second, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	cache.now = func() time.Time { return now }
	if _, err := cache.Lookup(t.Context(), "media.example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.Lookup(t.Context(), "media.example.com"); err != nil || resolver.calls != 1 {
		t.Fatalf("cached lookup error = %v, calls = %d", err, resolver.calls)
	}
	now = now.Add(time.Minute)
	resolver.err = errors.New("resolver unavailable")
	addresses, err := cache.Lookup(t.Context(), "media.example.com")
	if err != nil || len(addresses) != 1 {
		t.Fatalf("stale lookup = %v, %v", addresses, err)
	}
}

func TestDNSCacheRejectsUnsafeDestinations(t *testing.T) {
	t.Parallel()

	resolver := &fakeResolver{addresses: []netip.Addr{netip.MustParseAddr("fd00::1"), netip.MustParseAddr("2001:db8::1")}, ttl: time.Minute}
	cache, err := NewDNSCache(resolver, 10*time.Second, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cache.Lookup(t.Context(), "media.example.com"); err == nil {
		t.Fatal("cache accepted unsafe destinations")
	}
}
