package diagnose

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/netip"
	"time"
)

// EdgeProber confirms reachability by reaching a service through the operator's
// own IPv4 edge.
//
// The edge sits outside the home network and does not terminate TLS: it reads
// the requested name from the ClientHello and opens a fresh inbound connection
// to the published address. A handshake that completes through it therefore
// proves the same path a real client takes, including the customer-edge router
// that no host-local check can see. It needs no third party and no
// configuration beyond the edge the operator already runs.
//
// It cannot measure path MTU, because it never sends large frames, so it
// reports reachability only.
type EdgeProber struct {
	address netip.Addr
	timeout time.Duration
}

func NewEdgeProber(address netip.Addr) *EdgeProber {
	return &EdgeProber{address: address, timeout: 10 * time.Second}
}

func (p *EdgeProber) Probe(ctx context.Context, request ProbeRequest) (ProbeResult, error) {
	if request.ServerName == "" {
		return ProbeResult{}, fmt.Errorf("the edge dispatches on a TLS name, so probing needs one")
	}
	probeContext, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	dialer := &net.Dialer{}
	connection, err := dialer.DialContext(probeContext, "tcp4", netip.AddrPortFrom(p.address, request.Port).String())
	if err != nil {
		return ProbeResult{}, fmt.Errorf("connect to the edge at %s: %w", p.address, err)
	}
	defer func() { _ = connection.Close() }()

	// The edge relays this handshake to the published address. Certificate
	// verification is intentionally skipped: the question is whether the path
	// carries traffic, not whether the chain is trusted, and check judges the
	// certificate separately.
	client := tls.Client(connection, &tls.Config{ServerName: request.ServerName, InsecureSkipVerify: true})
	if err := client.HandshakeContext(probeContext); err != nil {
		return ProbeResult{Reachable: false}, nil
	}
	_ = client.Close()
	return ProbeResult{Reachable: true, PathMTUMeasured: false}, nil
}

// Describe names the vantage a report was verified from.
func (p *EdgeProber) Describe() string {
	return "the IPv4 edge at " + p.address.String()
}
