package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sirrobot01/bifrost/internal/diagnose"
)

func TestRunnerDoctorNeedsNoConfiguration(t *testing.T) {
	t.Parallel()

	// The whole point of doctor is that it runs before a config file exists,
	// so naming a missing interface must still produce a report rather than a
	// usage error about --config.
	var stdout, stderr bytes.Buffer
	runner := Runner{Stdout: &stdout, Stderr: &stderr, Version: "test"}
	if code := runner.Run(t.Context(), []string{"doctor", "--interface", "definitely-not-an-interface", "--offline"}); code != 1 {
		t.Fatalf("code = %d, want 1; stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "could not be inspected") {
		t.Fatalf("stdout = %s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "must be fixed") {
		t.Fatalf("stdout has no summary:\n%s", stdout.String())
	}
}

func TestRunnerDoctorEmitsJSONReport(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	runner := Runner{Stdout: &stdout, Stderr: &stderr, Version: "test"}
	runner.Run(t.Context(), []string{"doctor", "--interface", "definitely-not-an-interface", "--offline", "--json"})

	var report diagnose.Report
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("doctor --json is not valid JSON: %v\n%s", err, stdout.String())
	}
	if len(report.Findings) == 0 || report.Findings[0].Check != "interface" {
		t.Fatalf("findings = %+v", report.Findings)
	}
}

func TestDoctorSummaryCountsSeverities(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		findings []diagnose.Finding
		want     string
	}{
		{
			name:     "clean",
			findings: []diagnose.Finding{{Severity: diagnose.SeverityInfo}},
			want:     "This host can run Bifrost.",
		},
		{
			name:     "warnings only",
			findings: []diagnose.Finding{{Severity: diagnose.SeverityWarning}, {Severity: diagnose.SeverityWarning}},
			want:     "with 2 warnings to review",
		},
		{
			name:     "single error",
			findings: []diagnose.Finding{{Severity: diagnose.SeverityError}},
			want:     "1 problem must be fixed",
		},
		{
			name:     "errors and warnings",
			findings: []diagnose.Finding{{Severity: diagnose.SeverityError}, {Severity: diagnose.SeverityWarning}},
			want:     "1 problem must be fixed before Bifrost can publish a service, and 1 warning to review.",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var output bytes.Buffer
			if err := writeDoctorSummary(&output, diagnose.Report{Findings: test.findings}); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(output.String(), test.want) {
				t.Fatalf("summary = %q, want %q", output.String(), test.want)
			}
		})
	}
}
