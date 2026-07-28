package diagnose

import "testing"

func TestVerificationRequiresEveryService(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		findings []Finding
		want     Verification
	}{
		{
			name: "all reached",
			findings: []Finding{
				{Check: "external", Service: "media", Severity: SeverityInfo},
				{Check: "external", Service: "photos", Severity: SeverityInfo},
			},
			want: VerificationExternal,
		},
		{
			name: "one inconclusive",
			findings: []Finding{
				{Check: "external", Service: "media", Severity: SeverityInfo},
				{Check: "external", Service: "photos", Severity: SeverityWarning},
			},
			want: VerificationPartial,
		},
		{
			name: "one unreachable",
			findings: []Finding{
				{Check: "external", Service: "media", Severity: SeverityInfo},
				{Check: "external", Service: "photos", Severity: SeverityError},
			},
			want: VerificationUnreachable,
		},
		{
			name:     "none reached",
			findings: []Finding{{Check: "external", Service: "media", Severity: SeverityWarning}},
			want:     VerificationNone,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			report := Report{Findings: test.findings}
			report.classifyVerification()
			if report.Verification != test.want {
				t.Fatalf("verification = %q, want %q", report.Verification, test.want)
			}
		})
	}
}
