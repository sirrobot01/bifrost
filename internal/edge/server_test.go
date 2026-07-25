package edge

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
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
	if !server.admitConnection(first) {
		t.Fatal("first connection was not admitted")
	}
	if got := server.global.Active(); got != 1 {
		t.Fatalf("active = %d, want 1", got)
	}

	_, second := dialPair(t)
	if server.admitConnection(second) {
		t.Fatal("second connection was admitted past the global limit")
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
	if !server.admitConnection(first) {
		t.Fatal("first connection was not admitted")
	}
	server.global.Release()

	// The same source has spent its burst, so the next attempt is rate limited.
	_, second := dialPair(t)
	if server.admitConnection(second) {
		t.Fatal("a rate limited source was admitted")
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
