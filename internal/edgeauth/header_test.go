package edgeauth

import (
	"bytes"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestAuthenticatedProxyHeaderRoundTrip(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 25, 12, 0, 0, 0, time.UTC)
	key := bytes.Repeat([]byte{0x42}, 32)
	header, err := build(key, "media", netip.MustParseAddrPort("192.0.2.10:1234"), netip.MustParseAddrPort("198.51.100.2:443"), now, bytes.NewReader(bytes.Repeat([]byte{0x23}, 16)))
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := NewVerifier(key, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	verifier.now = func() time.Time { return now }
	metadata, err := verifier.Verify("media", header)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Source != netip.MustParseAddrPort("192.0.2.10:1234") || metadata.Destination != netip.MustParseAddrPort("198.51.100.2:443") {
		t.Fatalf("metadata = %+v", metadata)
	}
	if _, err := verifier.Verify("media", header); err == nil || !strings.Contains(err.Error(), "replayed") {
		t.Fatalf("replay error = %v", err)
	}
}

func TestAuthenticatedProxyHeaderRejectsTampering(t *testing.T) {
	t.Parallel()

	now := time.Now()
	key := bytes.Repeat([]byte{0x42}, 32)
	header, err := Build(key, "media", netip.MustParseAddrPort("192.0.2.10:1234"), netip.MustParseAddrPort("198.51.100.2:443"), now)
	if err != nil {
		t.Fatal(err)
	}
	header[20] ^= 1
	verifier, err := NewVerifier(key, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifier.Verify("media", header); err == nil || !strings.Contains(err.Error(), "signature") {
		t.Fatalf("error = %v", err)
	}
}

func TestAuthenticatedProxyHeaderRetainsFutureNonceForFullWindow(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, time.July, 25, 12, 0, 0, 0, time.UTC)
	key := bytes.Repeat([]byte{0x42}, 32)
	header, err := build(key, "media", netip.MustParseAddrPort("192.0.2.10:1234"), netip.MustParseAddrPort("198.51.100.2:443"), base.Add(30*time.Second), bytes.NewReader(bytes.Repeat([]byte{0x23}, 16)))
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := NewVerifier(key, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	now := base
	verifier.now = func() time.Time { return now }
	if _, err := verifier.Verify("media", header); err != nil {
		t.Fatal(err)
	}
	now = base.Add(31 * time.Second)
	if _, err := verifier.Verify("media", header); err == nil || !strings.Contains(err.Error(), "replayed") {
		t.Fatalf("replay error = %v", err)
	}
}

// The replay cache must discard nonces once they age past the window in which
// their timestamp could still be accepted, so that memory does not grow without
// bound while the edge keeps sending traffic.
func TestVerifierReplayCacheExpiresNonces(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, time.July, 25, 12, 0, 0, 0, time.UTC)
	key := bytes.Repeat([]byte{0x42}, 32)
	verifier, err := NewVerifier(key, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	now := base
	verifier.now = func() time.Time { return now }

	source := netip.MustParseAddrPort("192.0.2.10:1234")
	destination := netip.MustParseAddrPort("198.51.100.2:443")
	verify := func(index int) {
		nonce := bytes.Repeat([]byte{byte(index), byte(index >> 8)}, 8)
		header, err := build(key, "media", source, destination, now, bytes.NewReader(nonce))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := verifier.Verify("media", header); err != nil {
			t.Fatalf("verify at %v: %v", now, err)
		}
	}

	// Fill one full retention window, then keep going for several more.
	for index := range 400 {
		now = base.Add(time.Duration(index) * time.Second)
		verify(index)
	}
	if verifier.tracked > 200 {
		t.Fatalf("tracked = %d, want expiry to bound the cache well under the 400 nonces verified", verifier.tracked)
	}

	// A long silence must clear the cache entirely rather than strand entries.
	now = base.Add(time.Hour)
	verify(1000)
	if verifier.tracked != 1 {
		t.Fatalf("tracked = %d, want only the nonce just recorded", verifier.tracked)
	}
}

// Expiry must not sweep entries, so verification cost has to stay flat as the
// number of tracked nonces grows.
func TestVerifierCostDoesNotGrowWithTrackedNonces(t *testing.T) {
	t.Parallel()

	key := bytes.Repeat([]byte{0x42}, 32)
	source := netip.MustParseAddrPort("192.0.2.10:1234")
	destination := netip.MustParseAddrPort("198.51.100.2:443")

	measure := func(fill int) time.Duration {
		now := time.Date(2026, time.July, 25, 12, 0, 0, 0, time.UTC)
		verifier, err := NewVerifier(key, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		verifier.now = func() time.Time { return now }
		headers := make([][]byte, 0, fill)
		for index := range fill {
			nonce := bytes.Repeat([]byte{byte(index), byte(index >> 8), byte(index >> 16)}, 6)[:16]
			header, err := build(key, "media", source, destination, now, bytes.NewReader(nonce))
			if err != nil {
				t.Fatal(err)
			}
			headers = append(headers, header)
		}
		for _, header := range headers {
			if _, err := verifier.Verify("media", header); err != nil {
				t.Fatal(err)
			}
		}
		if verifier.tracked != fill {
			t.Fatalf("tracked = %d, want %d", verifier.tracked, fill)
		}
		// Re-verifying a known nonce exercises lookup against the full cache.
		const repetitions = 2000
		start := time.Now()
		for range repetitions {
			if _, err := verifier.Verify("media", headers[0]); err == nil {
				t.Fatal("expected a replay rejection")
			}
		}
		return time.Since(start) / repetitions
	}

	small := measure(2000)
	large := measure(50000)
	t.Logf("per Verify: 2k nonces = %v, 50k nonces = %v", small, large)
	// The linear sweep this replaced was ~25x slower at 50k than at 2k.
	if large > 8*small+time.Microsecond {
		t.Fatalf("cost scaled with cache size: %v at 2k nonces, %v at 50k", small, large)
	}
}
