package diagnose

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/sirrobot01/bifrost/internal/serviceaddr"
)

func findingFor(t *testing.T, report Report, check string) Finding {
	t.Helper()
	for _, finding := range report.Findings {
		if finding.Check == check {
			return finding
		}
	}
	t.Fatalf("report has no %q finding: %+v", check, report.Findings)
	return Finding{}
}

func preflightChecker(t *testing.T, dialErr error) *Checker {
	t.Helper()
	checker := NewChecker(fixedResolver{}, UnavailableFirewallAuditor(errors.New("unused")))
	checker.dial = func(context.Context, string, string) (net.Conn, error) {
		if dialErr != nil {
			return nil, dialErr
		}
		client, server := net.Pipe()
		t.Cleanup(func() { _ = server.Close() })
		return client, nil
	}
	return checker
}

func TestPreflightExplainsTemporaryAddresses(t *testing.T) {
	t.Parallel()

	checker := preflightChecker(t, nil)
	report, err := checker.Preflight(t.Context(), PreflightInput{
		Interface: "eth0",
		MTU:       1500,
		Candidates: []serviceaddr.Candidate{
			{Prefix: netip.MustParsePrefix("2001:db8::1/64"), Temporary: true},
		},
		SkipNetwork: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	finding := findingFor(t, report, "ipv6-prefix")
	if finding.Severity != SeverityError {
		t.Fatalf("severity = %s, want error", finding.Severity)
	}
	// The point of preflight is that the operator learns the cause, not just
	// that selection failed.
	if !strings.Contains(finding.Detail, "temporary privacy address") {
		t.Fatalf("detail = %q", finding.Detail)
	}
	if !strings.Contains(finding.Remediation, "use_tempaddr") {
		t.Fatalf("remediation = %q", finding.Remediation)
	}
	if report.Healthy() {
		t.Fatal("report is healthy without a usable prefix")
	}
}

func TestPreflightReportsUsablePrefixAndEgress(t *testing.T) {
	t.Parallel()

	checker := preflightChecker(t, nil)
	report, err := checker.Preflight(t.Context(), PreflightInput{
		Interface: "eth0",
		MTU:       1500,
		Candidates: []serviceaddr.Candidate{
			{Prefix: netip.MustParsePrefix("2001:db8::1/64"), ValidUntil: time.Now().Add(time.Hour)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if finding := findingFor(t, report, "ipv6-prefix"); finding.Severity != SeverityInfo {
		t.Fatalf("prefix finding = %+v", finding)
	}
	if finding := findingFor(t, report, "ipv6-egress"); finding.Severity != SeverityInfo {
		t.Fatalf("egress finding = %+v", finding)
	}
}

func TestPreflightFlagsBrokenEgress(t *testing.T) {
	t.Parallel()

	checker := preflightChecker(t, errors.New("network is unreachable"))
	report, err := checker.Preflight(t.Context(), PreflightInput{
		Interface:  "eth0",
		MTU:        1500,
		Candidates: []serviceaddr.Candidate{{Prefix: netip.MustParsePrefix("2001:db8::1/64")}},
	})
	if err != nil {
		t.Fatal(err)
	}

	finding := findingFor(t, report, "ipv6-egress")
	if finding.Severity != SeverityError {
		t.Fatalf("egress finding = %+v", finding)
	}
	if !strings.Contains(finding.Remediation, "default route") {
		t.Fatalf("remediation = %q", finding.Remediation)
	}
}

func TestPreflightSkipsNetworkWhenOffline(t *testing.T) {
	t.Parallel()

	checker := NewChecker(fixedResolver{}, UnavailableFirewallAuditor(errors.New("unused")))
	checker.dial = func(context.Context, string, string) (net.Conn, error) {
		t.Fatal("offline preflight dialled the network")
		return nil, nil
	}
	report, err := checker.Preflight(t.Context(), PreflightInput{
		Interface:   "eth0",
		MTU:         1500,
		Candidates:  []serviceaddr.Candidate{{Prefix: netip.MustParsePrefix("2001:db8::1/64")}},
		SkipNetwork: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if finding := findingFor(t, report, "ipv6-egress"); finding.Severity != SeverityWarning {
		t.Fatalf("egress finding = %+v", finding)
	}
}

func TestPreflightRequiresInterface(t *testing.T) {
	t.Parallel()

	checker := preflightChecker(t, nil)
	if _, err := checker.Preflight(t.Context(), PreflightInput{}); err == nil {
		t.Fatal("Preflight accepted an empty interface")
	}
}

func TestPreflightReportsMultiplePrefixes(t *testing.T) {
	t.Parallel()

	checker := preflightChecker(t, nil)
	report, err := checker.Preflight(t.Context(), PreflightInput{
		Interface: "eth0",
		MTU:       1500,
		Candidates: []serviceaddr.Candidate{
			{Prefix: netip.MustParsePrefix("2001:db8:1::1/64")},
			{Prefix: netip.MustParsePrefix("2001:db8:2::1/64")},
		},
		SkipNetwork: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	// With more than one eligible prefix the operator has a choice to make, so
	// the finding has to name prefix_override rather than silently pick one.
	finding := findingFor(t, report, "ipv6-prefix")
	if !strings.Contains(finding.Detail, "prefix_override") {
		t.Fatalf("detail = %q", finding.Detail)
	}
}

type stubProber struct {
	result ProbeResult
	err    error
	dialed bool
}

func (p *stubProber) Probe(_ context.Context, request ProbeRequest) (ProbeResult, error) {
	if p.err != nil {
		return ProbeResult{}, p.err
	}
	if p.result.Reachable {
		// A real probe proves reachability by connecting, so the stub must too.
		p.dialed = true
		connection, err := net.DialTimeout("tcp6", netip.AddrPortFrom(request.Address, request.Port).String(), 2*time.Second)
		if err != nil {
			return ProbeResult{Reachable: false}, nil
		}
		_ = connection.Close()
	}
	return p.result, nil
}

// Without a probe, doctor must say plainly that it never tested the one thing
// a host cannot observe about itself.
func TestPreflightReportsInboundUntested(t *testing.T) {
	t.Parallel()

	checker := NewChecker(nil, UnavailableFirewallAuditor(errors.New("unused")))
	report, err := checker.Preflight(t.Context(), PreflightInput{Interface: "lo", MTU: 1500, SkipNetwork: true})
	if err != nil {
		t.Fatal(err)
	}
	finding := findingFor(t, report, "inbound")
	if finding.Severity != SeverityWarning || !strings.Contains(finding.Summary, "not tested") {
		t.Fatalf("finding = %+v", finding)
	}
}

// With a probe that cannot reach the host, the router is named as the thing
// no local check can see.
func TestPreflightDetectsBlockedInbound(t *testing.T) {
	t.Parallel()

	checker := NewChecker(nil, UnavailableFirewallAuditor(errors.New("unused")))
	report, err := checker.Preflight(t.Context(), PreflightInput{
		Interface:   "lo",
		MTU:         1500,
		Candidates:  []serviceaddr.Candidate{{Prefix: netip.MustParsePrefix("2001:db8:1::10/64")}},
		Probe:       &stubProber{result: ProbeResult{Reachable: false}},
		SkipNetwork: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	finding := findingFor(t, report, "inbound")
	if finding.Severity != SeverityError || !strings.Contains(finding.Remediation, "customer-edge router") {
		t.Fatalf("finding = %+v", finding)
	}
}
