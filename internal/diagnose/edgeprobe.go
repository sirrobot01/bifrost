package diagnose

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strings"
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
	addresses []netip.Addr
	timeout   time.Duration
}

func NewEdgeProber(addresses ...netip.Addr) *EdgeProber {
	return &EdgeProber{addresses: append([]netip.Addr(nil), addresses...), timeout: 10 * time.Second}
}

// Supports prevents a deployment-wide edge from being treated as proof for a
// service that has no A records pointing at that edge.
func (p *EdgeProber) Supports(request ProbeRequest) bool {
	return len(request.EdgeAddresses) > 0
}

func (p *EdgeProber) Probe(ctx context.Context, request ProbeRequest) (ProbeResult, error) {
	if request.ServerName == "" {
		return ProbeResult{}, fmt.Errorf("the edge dispatches on a TLS name, so probing needs one")
	}
	if len(p.addresses) == 0 {
		return ProbeResult{}, fmt.Errorf("probing needs at least one edge address")
	}
	probeContext, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	type outcome struct {
		address netip.Addr
		err     error
	}
	outcomes := make(chan outcome, len(p.addresses))
	for _, address := range p.addresses {
		go func() {
			outcomes <- outcome{address: address, err: probeEdge(probeContext, address, request)}
		}()
	}
	var failures []string
	for range p.addresses {
		result := <-outcomes
		if result.err != nil {
			failures = append(failures, fmt.Sprintf("edge %s: %v", result.address, result.err))
		}
	}
	if len(failures) > 0 {
		slices.Sort(failures)
		return ProbeResult{Reachable: false, Detail: strings.Join(failures, "; ")}, nil
	}
	return ProbeResult{Reachable: true, PathMTUMeasured: false}, nil
}

func probeEdge(ctx context.Context, address netip.Addr, request ProbeRequest) error {
	if !address.Is4() {
		return fmt.Errorf("address is not IPv4")
	}
	dialer := &net.Dialer{}
	connection, err := dialer.DialContext(ctx, "tcp4", netip.AddrPortFrom(address, request.Port).String())
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer func() { _ = connection.Close() }()

	// The edge relays this handshake to the published address. Certificate
	// verification is intentionally skipped: the question is whether the path
	// carries traffic, not whether the chain is trusted, and check judges the
	// certificate separately.
	client := tls.Client(connection, &tls.Config{ServerName: request.ServerName, InsecureSkipVerify: true})
	if err := client.HandshakeContext(ctx); err != nil {
		return fmt.Errorf("TLS handshake: %w", err)
	}
	_ = client.Close()
	return nil
}

// Describe names the vantage a report was verified from.
func (p *EdgeProber) Describe() string {
	addresses := make([]string, 0, len(p.addresses))
	for _, address := range p.addresses {
		addresses = append(addresses, address.String())
	}
	if len(addresses) == 1 {
		return "the IPv4 edge at " + addresses[0]
	}
	return "the IPv4 edges at " + strings.Join(addresses, ", ")
}
