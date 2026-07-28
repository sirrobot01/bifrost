package diagnose

import "time"

type Severity string

const (
	SeverityInfo    Severity = "info"
	SeverityWarning Severity = "warning"
	SeverityError   Severity = "error"
)

type Finding struct {
	Check string `json:"check"`
	// Service names the service a finding belongs to; empty for host findings.
	Service     string   `json:"service,omitempty"`
	Severity    Severity `json:"severity"`
	Summary     string   `json:"summary"`
	Detail      string   `json:"detail,omitempty"`
	Remediation string   `json:"remediation,omitempty"`
}

// Verification records whether a report contains evidence gathered from
// outside the host. Everything a host can observe about itself is compatible
// with being unreachable, so a report without external evidence must not
// claim a service is published.
type Verification string

const (
	// VerificationNone means nothing outside the host was consulted.
	VerificationNone Verification = "none"
	// VerificationExternal means an external vantage reached the service.
	VerificationExternal Verification = "external"
	// VerificationUnreachable means an external vantage tried and failed.
	VerificationUnreachable Verification = "unreachable"
)

type Report struct {
	GeneratedAt time.Time `json:"generated_at"`
	// Verification states how far the report's evidence reaches.
	Verification Verification `json:"verification"`
	Findings     []Finding    `json:"findings"`
}

// classifyVerification derives the report's reach from the external findings,
// so the verdict cannot drift from the evidence.
func (r *Report) classifyVerification() {
	r.Verification = VerificationNone
	for _, finding := range r.Findings {
		if finding.Check != "external" {
			continue
		}
		switch finding.Severity {
		case SeverityError:
			r.Verification = VerificationUnreachable
			return
		case SeverityInfo:
			r.Verification = VerificationExternal
		}
	}
}

func (r Report) Healthy() bool {
	for _, finding := range r.Findings {
		if finding.Severity == SeverityError {
			return false
		}
	}
	return true
}
