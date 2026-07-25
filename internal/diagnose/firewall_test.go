package diagnose

import (
	"slices"
	"testing"
)

func TestFirewallFindingsWarnAboutAuthoritativeDropAndICMPv6(t *testing.T) {
	t.Parallel()

	findings := firewallFindings([]FirewallChain{{
		Family:            "inet",
		Table:             "filter",
		Name:              "input",
		Priority:          0,
		DropPolicy:        true,
		AcceptedICMPTypes: []uint8{1, 3, 4},
		AnalysisComplete:  true,
	}})

	if len(findings) != 2 {
		t.Fatalf("findings = %d, want 2", len(findings))
	}
	if findings[0].Check != "firewall" || findings[0].Severity != SeverityWarning {
		t.Fatalf("firewall finding = %+v", findings[0])
	}
	if findings[1].Check != "icmpv6" || findings[1].Severity != SeverityWarning {
		t.Fatalf("ICMPv6 finding = %+v", findings[1])
	}
}

func TestFirewallFindingsRecognizeEssentialICMPv6(t *testing.T) {
	t.Parallel()

	findings := firewallFindings([]FirewallChain{{
		Family:            "ip6",
		Table:             "filter",
		Name:              "input",
		DropPolicy:        true,
		AcceptedICMPTypes: []uint8{4, 2, 1, 3},
		AnalysisComplete:  true,
	}})

	if len(findings) != 2 || findings[1].Severity != SeverityInfo {
		t.Fatalf("findings = %+v", findings)
	}
}

func TestMissingICMPTypes(t *testing.T) {
	t.Parallel()

	if got, want := missingICMPTypes([]uint8{2, 4}), []uint8{1, 3}; !slices.Equal(got, want) {
		t.Fatalf("missing types = %v, want %v", got, want)
	}
}

func TestMTUFinding(t *testing.T) {
	t.Parallel()

	tests := []struct {
		mtu      int
		severity Severity
	}{
		{mtu: 1279, severity: SeverityError},
		{mtu: 1492, severity: SeverityWarning},
		{mtu: 1500, severity: SeverityInfo},
	}
	for _, test := range tests {
		if got := mtuFinding(test.mtu).Severity; got != test.severity {
			t.Fatalf("MTU %d severity = %s, want %s", test.mtu, got, test.severity)
		}
	}
}
