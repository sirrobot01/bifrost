package diagnose

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strings"
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

func TestDNSFindingCrossChecksAuthoritative(t *testing.T) {
	t.Parallel()

	address := netip.MustParseAddr("2001:db8::1")
	service := Service{Name: "media", DNSName: "media.example.com", Address: address, Port: 443}
	tests := []struct {
		name          string
		resolver      fixedResolver
		authoritative []netip.Addr
		authErr       error
		wantSeverity  Severity
		wantSummary   string
	}{
		{
			name:         "resolver agrees",
			resolver:     fixedResolver{addresses: []netip.Addr{address}},
			wantSeverity: SeverityInfo,
			wantSummary:  "expected IPv6 address",
		},
		{
			name:          "stale local resolver",
			resolver:      fixedResolver{err: errors.New("no such host")},
			authoritative: []netip.Addr{address},
			wantSeverity:  SeverityWarning,
			wantSummary:   "the local resolver does not",
		},
		{
			name:         "unpublished at authoritative",
			resolver:     fixedResolver{err: errors.New("no such host")},
			wantSeverity: SeverityError,
			wantSummary:  "authoritative nameserver does not serve",
		},
		{
			name:         "authoritative unreachable",
			resolver:     fixedResolver{err: errors.New("no such host")},
			authErr:      errors.New("no NS records found"),
			wantSeverity: SeverityError,
			wantSummary:  "DNS lookup failed",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			checker := NewChecker(test.resolver, UnavailableFirewallAuditor(errors.New("unused")))
			checker.authoritativeAAAA = func(context.Context, string) ([]netip.Addr, string, error) {
				return test.authoritative, "ns1.example.net", test.authErr
			}
			finding := checker.dnsFinding(t.Context(), service)
			if finding.Severity != test.wantSeverity || !strings.Contains(finding.Summary, test.wantSummary) {
				t.Fatalf("finding = %+v", finding)
			}
		})
	}
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
