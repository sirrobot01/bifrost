package diagnose

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"net/netip"
	"testing"
	"time"
)

// testTLSListener stands in for the path a probe exercises: something outside
// the host that answers a TLS handshake for the published name.
func testTLSListener(t *testing.T) net.Listener {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "media.example.com"},
		DNSNames:     []string{"media.example.com"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := tls.Listen("tcp4", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
	})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				_ = connection.(*tls.Conn).Handshake()
				_ = connection.Close()
			}()
		}
	}()
	t.Cleanup(func() { _ = listener.Close() })
	return listener
}

func TestEdgeProberConfirmsReachability(t *testing.T) {
	t.Parallel()

	listener := testTLSListener(t)
	port := uint16(listener.Addr().(*net.TCPAddr).Port)
	prober := NewEdgeProber(netip.MustParseAddr("127.0.0.1"))

	result, err := prober.Probe(t.Context(), ProbeRequest{Port: port, ServerName: "media.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Reachable {
		t.Fatal("a completed handshake through the edge was not reported as reachable")
	}
	// This prober never sends large frames, so it must not imply a PMTU verdict.
	if result.PathMTUMeasured {
		t.Fatal("the edge prober claimed to have measured path MTU")
	}
}

func TestEdgeProberReportsUnreachable(t *testing.T) {
	t.Parallel()

	// Nothing is listening here, which is what a blocked inbound path looks
	// like from outside: the probe must report it rather than error out, so
	// check renders it as a finding.
	prober := NewEdgeProber(netip.MustParseAddr("127.0.0.1"))
	prober.timeout = 2 * time.Second
	result, err := prober.Probe(t.Context(), ProbeRequest{Port: 1, ServerName: "media.example.com"})
	if err == nil && result.Reachable {
		t.Fatal("an unreachable service was reported as reachable")
	}
}

func TestEdgeProberNeedsAServerName(t *testing.T) {
	t.Parallel()

	prober := NewEdgeProber(netip.MustParseAddr("192.0.2.1"))
	if _, err := prober.Probe(t.Context(), ProbeRequest{Port: 443}); err == nil {
		t.Fatal("probing without a name succeeded, but the edge dispatches on one")
	}
}
