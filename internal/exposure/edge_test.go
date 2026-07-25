package exposure

import (
	"bytes"
	"io"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/sirrobot01/bifrost/internal/edgeauth"
)

func TestInspectEdgeHeaderAuthenticatesAndConsumesMetadata(t *testing.T) {
	t.Parallel()

	key := bytes.Repeat([]byte{0x42}, 32)
	header, err := edgeauth.Build(key, "media.example.com", netip.MustParseAddrPort("192.0.2.10:1234"), netip.MustParseAddrPort("198.51.100.2:443"), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := edgeauth.NewVerifier(key, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	server, client := net.Pipe()
	defer func() { _ = server.Close() }()
	defer func() { _ = client.Close() }()
	go func() { _, _ = client.Write(append(header, []byte("payload")...)) }()

	connection, metadata, err := inspectEdgeHeader(server, "media.example.com", verifier, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if metadata == nil || metadata.Source != netip.MustParseAddrPort("192.0.2.10:1234") {
		t.Fatalf("metadata = %+v", metadata)
	}
	payload := make([]byte, len("payload"))
	if _, err := io.ReadFull(connection, payload); err != nil {
		t.Fatal(err)
	}
	if string(payload) != "payload" {
		t.Fatalf("payload = %q", payload)
	}
}
