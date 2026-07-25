package diagnose

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"
)

type fixedResolver struct {
	addresses []netip.Addr
	err       error
}

func (r fixedResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return r.addresses, r.err
}

type fixedProber struct {
	result ProbeResult
	err    error
}

func (p fixedProber) Probe(context.Context, ProbeRequest) (ProbeResult, error) {
	return p.result, p.err
}

func TestServiceFindingsIdentifyPMTUBlackhole(t *testing.T) {
	t.Parallel()

	address := netip.MustParseAddr("2001:db8::1")
	checker := NewChecker(fixedResolver{addresses: []netip.Addr{address}}, UnavailableFirewallAuditor(errors.New("unused")))
	findings := checker.serviceFindings(t.Context(), Service{Name: "media", DNSName: "media.example.com", Address: address, Port: 443}, nil, 1500, fixedProber{
		result: ProbeResult{Reachable: true, PathMTU: 1280, PacketTooBigWorks: false},
	})

	if len(findings) != 4 || findings[3].Check != "pmtu" || findings[3].Severity != SeverityError {
		t.Fatalf("findings = %+v", findings)
	}
}

func TestServiceFindingsCheckExactListener(t *testing.T) {
	t.Parallel()

	address := netip.MustParseAddr("2001:db8::1")
	checker := NewChecker(fixedResolver{addresses: []netip.Addr{address}}, UnavailableFirewallAuditor(errors.New("unused")))
	checker.dial = func(_ context.Context, network, target string) (net.Conn, error) {
		if network != "tcp6" || target != "[2001:db8::1]:443" {
			t.Fatalf("dial %s %s", network, target)
		}
		return nil, errors.New("connection refused")
	}
	findings := checker.serviceFindings(t.Context(), Service{Name: "media", DNSName: "media.example.com", Address: address, Port: 443, CheckLocal: true}, map[netip.Addr]struct{}{address: {}}, 1500, nil)

	if len(findings) < 3 || findings[2].Check != "listener" || findings[2].Severity != SeverityError {
		t.Fatalf("findings = %+v", findings)
	}
}
