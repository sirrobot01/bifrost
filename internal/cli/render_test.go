package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sirrobot01/bifrost/internal/diagnose"
)

func TestWriteFindingsPlainFormat(t *testing.T) {
	var output bytes.Buffer
	report := diagnose.Report{Findings: []diagnose.Finding{
		{Check: "platform", Severity: diagnose.SeverityInfo, Summary: "host runs Linux"},
		{
			Check:       "firewall",
			Severity:    diagnose.SeverityError,
			Summary:     "inbound traffic is dropped",
			Detail:      "input chain policy is drop",
			Remediation: "add an accept rule for the published ports",
		},
	}}
	if err := writeFindings(&output, report); err != nil {
		t.Fatal(err)
	}
	want := "INFO    platform    host runs Linux\n" +
		"ERROR   firewall    inbound traffic is dropped\n" +
		findingIndent + "input chain policy is drop\n" +
		findingIndent + "fix: add an accept rule for the published ports\n" +
		findingIndent + "docs: " + troubleshootingURL + "#firewall\n"
	if output.String() != want {
		t.Fatalf("output = %q, want %q", output.String(), want)
	}
}

func TestWriteFindingsPipedOutputHasNoEscapes(t *testing.T) {
	var output bytes.Buffer
	report := diagnose.Report{Findings: []diagnose.Finding{
		{Check: "mtu", Severity: diagnose.SeverityWarning, Summary: "MTU is low", Remediation: "raise it"},
	}}
	if err := writeFindings(&output, report); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "\x1b[") {
		t.Fatalf("piped output contains escape sequences: %q", output.String())
	}
	if !strings.Contains(output.String(), "docs: "+troubleshootingURL+"#mtu") {
		t.Fatalf("warning finding lacks a docs link: %q", output.String())
	}
}

func TestWriteFindingsInfoHasNoDocsLink(t *testing.T) {
	var output bytes.Buffer
	report := diagnose.Report{Findings: []diagnose.Finding{
		{Check: "platform", Severity: diagnose.SeverityInfo, Summary: "host runs Linux"},
	}}
	if err := writeFindings(&output, report); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "docs:") {
		t.Fatalf("info finding has a docs link: %q", output.String())
	}
}

func TestWriteFindingColored(t *testing.T) {
	var output bytes.Buffer
	finding := diagnose.Finding{
		Check:       "firewall",
		Severity:    diagnose.SeverityError,
		Summary:     "inbound traffic is dropped",
		Detail:      "input chain policy is drop",
		Remediation: "add an accept rule",
	}
	if err := writeFinding(&output, ansiPalette, finding); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	if !strings.Contains(text, ansiPalette.red+"✗ ERROR  "+ansiPalette.reset) {
		t.Fatalf("error line lacks the red glyph column: %q", text)
	}
	// Continuation lines sit two characters deeper to stay under the summary,
	// which the glyph and its space pushed right.
	if !strings.Contains(text, findingIndent+"  input chain policy is drop\n") {
		t.Fatalf("detail is not aligned under the summary: %q", text)
	}
}

func TestWriteServiceReportGroupsFindings(t *testing.T) {
	var output bytes.Buffer
	report := diagnose.Report{Findings: []diagnose.Finding{
		{Check: "mtu", Severity: diagnose.SeverityInfo, Summary: "interface MTU is suitable for IPv6", Detail: "MTU 1500"},
		{Check: "client-ip", Severity: diagnose.SeverityWarning, Service: "plex", Summary: "splice mode hides the client address from the backend", Remediation: "prefer direct mode"},
		{Check: "address", Severity: diagnose.SeverityInfo, Service: "plex", Summary: "service address is present on the host"},
		{Check: "dns", Severity: diagnose.SeverityInfo, Service: "plex", Summary: "DNS contains the expected IPv6 address"},
		{Check: "client-ip", Severity: diagnose.SeverityWarning, Service: "emby", Summary: "splice mode hides the client address from the backend", Remediation: "prefer direct mode"},
		{Check: "address", Severity: diagnose.SeverityError, Service: "emby", Summary: "service address is missing from the host"},
		{Check: "firewall", Severity: diagnose.SeverityInfo, Summary: "no nftables IPv6 input base chain with a drop policy was found"},
	}}
	if err := writeFindings(&output, report); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	t.Logf("rendered:\n%s", text)

	if !strings.Contains(text, "plex  ok: address dns  warn: client-ip") {
		t.Fatalf("missing plex summary line:\n%s", text)
	}
	if !strings.Contains(text, "emby  warn: client-ip  FAIL: address") {
		t.Fatalf("missing emby summary line:\n%s", text)
	}
	if !strings.Contains(text, "emby: service address is missing from the host") {
		t.Fatalf("missing error detail:\n%s", text)
	}
	// The identical warning must appear once, naming both services.
	if got := strings.Count(text, "splice mode hides the client address"); got != 1 {
		t.Fatalf("warning appears %d times, want 1:\n%s", got, text)
	}
	if !strings.Contains(text, "services: emby, plex") && !strings.Contains(text, "services: plex, emby") {
		t.Fatalf("grouped warning lacks the service list:\n%s", text)
	}
	// Passing INFO details stay out of the flat list.
	if strings.Contains(text, "INFO    address") {
		t.Fatalf("per-service INFO rendered as a full block:\n%s", text)
	}
}

func TestWriteCheckSummary(t *testing.T) {
	tests := []struct {
		name     string
		findings []diagnose.Finding
		want     string
	}{
		{
			name:     "clean",
			findings: []diagnose.Finding{{Severity: diagnose.SeverityInfo}},
			want:     "All checks passed.",
		},
		{
			name:     "warnings only",
			findings: []diagnose.Finding{{Check: "mtu", Severity: diagnose.SeverityWarning}, {Check: "privileges", Severity: diagnose.SeverityWarning}},
			want:     "All checks passed, with 2 warnings to review.",
		},
		{
			name:     "single error",
			findings: []diagnose.Finding{{Severity: diagnose.SeverityError}},
			want:     "1 problem blocks the published path.",
		},
		{
			name:     "errors and warnings",
			findings: []diagnose.Finding{{Severity: diagnose.SeverityError}, {Severity: diagnose.SeverityError}, {Severity: diagnose.SeverityWarning}},
			want:     "2 problems block the published path, and 1 warning to review.",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			if err := writeCheckSummary(&output, diagnose.Report{Findings: test.findings}); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(output.String(), test.want) {
				t.Fatalf("summary = %q, want %q", output.String(), test.want)
			}
		})
	}
}
