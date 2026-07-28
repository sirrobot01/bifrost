package edge

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sirrobot01/bifrost/internal/edgeauth"
)

func TestEdgeForwardsAuthenticatedClientHello(t *testing.T) {
	t.Parallel()

	key := bytes.Repeat([]byte{0x42}, 32)
	keyPath := filepath.Join(t.TempDir(), "edge.key")
	if err := os.WriteFile(keyPath, key, 0o600); err != nil {
		t.Fatal(err)
	}
	config := Config{Version: 1, Listen: "127.0.0.1:443", Allow: []string{"media.example.com"}, KeyFile: keyPath}
	config.applyDefaults()
	resolver := &fakeResolver{addresses: []netip.Addr{netip.MustParseAddr("2001:4860::10")}, ttl: time.Minute}
	server, err := NewServer(config, resolver, nil)
	if err != nil {
		t.Fatal(err)
	}
	backendServer, backendClient := net.Pipe()
	defer func() { _ = backendClient.Close() }()
	server.dialHome = func(context.Context, []netip.Addr, uint16) (net.Conn, error) { return backendServer, nil }

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	client, err := net.Dial("tcp4", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	edgeClient, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	go server.handle(t.Context(), edgeClient, route{tls: true, port: 443})

	hello := testClientHello("media.example.com")
	if _, err := client.Write(append(hello, []byte("payload")...)); err != nil {
		t.Fatal(err)
	}
	header := make([]byte, edgeauth.HeaderSize)
	if _, err := io.ReadFull(backendClient, header); err != nil {
		t.Fatal(err)
	}
	verifier, err := edgeauth.NewVerifier(key, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifier.Verify("media.example.com", header); err != nil {
		t.Fatal(err)
	}
	content := make([]byte, len(hello)+len("payload"))
	if _, err := io.ReadFull(backendClient, content); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(content[:len(hello)], hello) || string(content[len(hello):]) != "payload" {
		t.Fatalf("content = %x", content)
	}
}

func newAdmissionTestServer(t *testing.T, mutate func(*Config)) *Server {
	t.Helper()

	keyPath := filepath.Join(t.TempDir(), "edge.key")
	if err := os.WriteFile(keyPath, bytes.Repeat([]byte{0x42}, 32), 0o600); err != nil {
		t.Fatal(err)
	}
	config := Config{Version: 1, Listen: "127.0.0.1:443", Allow: []string{"media.example.com"}, KeyFile: keyPath}
	config.applyDefaults()
	if mutate != nil {
		mutate(&config)
	}
	resolver := &fakeResolver{addresses: []netip.Addr{netip.MustParseAddr("2001:4860::10")}, ttl: time.Minute}
	server, err := NewServer(config, resolver, nil)
	if err != nil {
		t.Fatal(err)
	}
	return server
}

// dialPair returns a connected client/server pair over real TCP so that
// RemoteAddr carries a *net.TCPAddr, as the accept path requires.
func dialPair(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	client, err := net.Dial("tcp4", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = client.Close()
		_ = accepted.Close()
	})
	return client, accepted
}

// Admission must hold a global slot only for connections it accepts, and must
// hand the slot back on every rejection path.
func TestEdgeAdmissionAccountsGlobalSlots(t *testing.T) {
	t.Parallel()

	server := newAdmissionTestServer(t, func(c *Config) { c.MaxConnections = 1 })

	_, first := dialPair(t)
	if _, admitted := server.admitConnection(first); !admitted {
		t.Fatal("first connection was not admitted")
	}
	if got := server.global.Active(); got != 1 {
		t.Fatalf("active = %d, want 1", got)
	}

	_, second := dialPair(t)
	reason, admitted := server.admitConnection(second)
	if admitted {
		t.Fatal("second connection was admitted past the global limit")
	}
	if !strings.Contains(reason, "connection limit") {
		t.Fatalf("reason = %q, want it to name the connection limit", reason)
	}
	if got := server.global.Active(); got != 1 {
		t.Fatalf("active = %d after a rejected connection, want 1", got)
	}

	server.global.Release()
	if got := server.global.Active(); got != 0 {
		t.Fatalf("active = %d after release, want 0", got)
	}
}

// A source turned away by the rate limiter must not strand the global slot that
// admission speculatively acquired.
func TestEdgeAdmissionReleasesSlotWhenRateLimited(t *testing.T) {
	t.Parallel()

	server := newAdmissionTestServer(t, func(c *Config) {
		c.MaxConnections = 16
		c.RatePerMinute = 1
		c.RateBurst = 1
	})

	_, first := dialPair(t)
	if _, admitted := server.admitConnection(first); !admitted {
		t.Fatal("first connection was not admitted")
	}
	server.global.Release()

	// The same source has spent its burst, so the next attempt is rate limited.
	_, second := dialPair(t)
	reason, admitted := server.admitConnection(second)
	if admitted {
		t.Fatal("a rate limited source was admitted")
	}
	// The reason reaches the log, so a refused operator is not left guessing.
	if !strings.Contains(reason, "rate limit") {
		t.Fatalf("reason = %q, want it to name the rate limit", reason)
	}
	if got := server.global.Active(); got != 0 {
		t.Fatalf("active = %d, want the speculatively held slot released", got)
	}
}

// Connections shed on the accept path must be closed rather than left open, and
// must not consume a goroutine waiting to discover they are over the limit.
func TestEdgeServeClosesConnectionsOverTheLimit(t *testing.T) {
	t.Parallel()

	server := newAdmissionTestServer(t, func(c *Config) { c.MaxConnections = 1 })
	backendServer, backendClient := net.Pipe()
	defer func() { _ = backendClient.Close() }()
	defer func() { _ = backendServer.Close() }()
	server.dialHome = func(context.Context, []netip.Addr, uint16) (net.Conn, error) { return backendServer, nil }

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() { _ = server.serve(ctx, listener, route{tls: true, port: 443}) }()

	// Occupy the single slot and keep it occupied by never completing the handshake.
	held, err := net.Dial("tcp4", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Close() }()
	deadline := time.Now().Add(2 * time.Second)
	for server.global.Active() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := server.global.Active(); got != 1 {
		t.Fatalf("active = %d, want the first connection to hold the only slot", got)
	}

	over, err := net.Dial("tcp4", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = over.Close() }()
	if err := over.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := over.Read(make([]byte, 1)); err == nil {
		t.Fatal("connection over the limit stayed open")
	} else if errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatal("connection over the limit was neither served nor closed")
	}
	if got := server.global.Active(); got != 1 {
		t.Fatalf("active = %d after shedding, want 1", got)
	}
}

// TestEdgeFollowsPrefixChangeAfterDialFailure reproduces a production failure:
// a home prefix change republished DNS within seconds, but the edge held the
// withdrawn addresses until the record TTL expired, which was an hour on a
// provider enforcing a long minimum. Every request failed in the meantime.
func TestEdgeFollowsPrefixChangeAfterDialFailure(t *testing.T) {
	t.Parallel()

	oldAddress := netip.MustParseAddr("2001:4860:1::10")
	newAddress := netip.MustParseAddr("2606:4700:2::20")
	resolver := &fakeResolver{addresses: []netip.Addr{oldAddress}, ttl: time.Hour}

	keyPath := filepath.Join(t.TempDir(), "edge.key")
	if err := os.WriteFile(keyPath, bytes.Repeat([]byte{0x42}, 32), 0o600); err != nil {
		t.Fatal(err)
	}
	configFile := Config{Version: 1, Listen: "127.0.0.1:0", Allow: []string{"media.example.com"}, KeyFile: keyPath}
	configFile.applyDefaults()
	server, err := NewServer(configFile, resolver, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}

	// Warm the cache with the pre-rotation answer, as a live edge would have.
	if _, err := server.cache.Lookup(t.Context(), "media.example.com"); err != nil {
		t.Fatal(err)
	}

	// The prefix rotates: DNS now serves the new address, but the cache is
	// pinned to the old one for an hour.
	resolver.addresses = []netip.Addr{newAddress}

	var dialled []netip.Addr
	server.dialHome = func(_ context.Context, addresses []netip.Addr, _ uint16) (net.Conn, error) {
		dialled = append(dialled, addresses...)
		if addresses[0] == newAddress {
			client, _ := net.Pipe()
			return client, nil
		}
		return nil, errors.New("no route to host")
	}

	addresses, err := server.cache.Lookup(t.Context(), "media.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if addresses[0] != oldAddress {
		t.Fatalf("cache did not hold the stale answer, so the test proves nothing")
	}
	if _, err := server.dialHome(t.Context(), addresses, 443); err == nil {
		t.Fatal("the stale address dialled successfully")
	}
	// This is the fix: a failed dial drops the entry so the next lookup sees
	// the republished record instead of waiting out the TTL.
	server.cache.Invalidate("media.example.com")
	retried, err := server.cache.Lookup(t.Context(), "media.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if retried[0] != newAddress {
		t.Fatalf("after invalidation the cache returned %v, want the republished %v", retried, newAddress)
	}
	if _, err := server.dialHome(t.Context(), retried, 443); err != nil {
		t.Fatalf("dialling the republished address failed: %v", err)
	}
}
