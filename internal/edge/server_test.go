package edge

import (
	"bytes"
	"context"
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
