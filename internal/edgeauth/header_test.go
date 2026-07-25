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
