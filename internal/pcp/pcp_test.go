package pcp

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"net/netip"
	"testing"
	"time"
)

// fakeServer answers one PCP request with the given result code and lifetime,
// and hands back what it received so the wire format can be asserted.
func fakeServer(t *testing.T, code uint8, lifetime uint32) (netip.Addr, uint16, <-chan []byte) {
	t.Helper()

	connection, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv6loopback, Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	received := make(chan []byte, 1)
	go func() {
		buffer := make([]byte, 1500)
		read, sender, err := connection.ReadFromUDP(buffer)
		if err != nil {
			return
		}
		received <- append([]byte(nil), buffer[:read]...)
		response := make([]byte, 60)
		response[0] = version
		response[1] = opcodeMap | 0x80
		response[3] = code
		binary.BigEndian.PutUint32(response[8:12], lifetime)
		_, _ = connection.WriteToUDP(response, sender)
	}()
	address := connection.LocalAddr().(*net.UDPAddr)
	return netip.MustParseAddr("::1"), uint16(address.Port), received
}

func newTestClient(t *testing.T, port uint16) *Client {
	t.Helper()
	client := NewClient(netip.MustParseAddr("::1"))
	client.serverPortOverride = port
	client.timeout = 2 * time.Second
	return client
}

func TestRequestSendsAWellFormedMapping(t *testing.T) {
	t.Parallel()

	_, port, received := fakeServer(t, resultSuccess, 3600)
	client := newTestClient(t, port)
	internal := netip.MustParseAddr("2001:db8::10")
	mapping := Mapping{Internal: internal, Port: 443, Lifetime: time.Hour, Nonce: [12]byte{1, 2, 3}}

	granted, err := client.Request(t.Context(), mapping)
	if err != nil {
		t.Fatal(err)
	}
	if granted != time.Hour {
		t.Fatalf("granted = %v, want the lifetime the router returned", granted)
	}

	payload := <-received
	if len(payload) != requestSize+mapPayload {
		t.Fatalf("request is %d bytes, want %d", len(payload), requestSize+mapPayload)
	}
	if payload[0] != version || payload[1] != opcodeMap {
		t.Fatalf("version/opcode = %d/%d", payload[0], payload[1])
	}
	if got := binary.BigEndian.Uint32(payload[4:8]); got != 3600 {
		t.Fatalf("requested lifetime = %d, want 3600", got)
	}
	if got, _ := netip.AddrFromSlice(payload[8:24]); got != internal {
		t.Fatalf("internal address = %v, want %v", got, internal)
	}
	body := payload[24:]
	if body[12] != protocolTCP {
		t.Fatalf("protocol = %d, want TCP", body[12])
	}
	if got := binary.BigEndian.Uint16(body[16:18]); got != 443 {
		t.Fatalf("internal port = %d", got)
	}
}

// A router that refuses must produce an error the operator can act on, not a
// silent failure and not a claim that the pinhole exists.
func TestRequestReportsRefusal(t *testing.T) {
	t.Parallel()

	_, port, _ := fakeServer(t, resultNotAuthorized, 0)
	client := newTestClient(t, port)
	_, err := client.Request(t.Context(), Mapping{Internal: netip.MustParseAddr("2001:db8::10"), Port: 443, Lifetime: time.Hour})
	if err == nil {
		t.Fatal("a refused mapping was reported as success")
	}
	if errors.Is(err, ErrUnsupported) {
		t.Fatalf("refusal was classed as absence of PCP: %v", err)
	}
}

// Silence is the common case, and it must be distinguishable from refusal so
// the caller can fall back to advisory output without alarming anyone.
func TestRequestTreatsSilenceAsUnsupported(t *testing.T) {
	t.Parallel()

	connection, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv6loopback, Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = connection.Close() }()
	client := newTestClient(t, uint16(connection.LocalAddr().(*net.UDPAddr).Port))
	client.timeout = 300 * time.Millisecond

	_, err = client.Request(t.Context(), Mapping{Internal: netip.MustParseAddr("2001:db8::10"), Port: 443, Lifetime: time.Hour})
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
}

func TestReleaseAsksForZeroLifetime(t *testing.T) {
	t.Parallel()

	_, port, received := fakeServer(t, resultSuccess, 0)
	client := newTestClient(t, port)
	if err := client.Release(context.Background(), Mapping{Internal: netip.MustParseAddr("2001:db8::10"), Port: 443, Lifetime: time.Hour}); err != nil {
		t.Fatal(err)
	}
	payload := <-received
	if got := binary.BigEndian.Uint32(payload[4:8]); got != 0 {
		t.Fatalf("release lifetime = %d, want 0", got)
	}
}
