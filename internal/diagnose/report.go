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

type Report struct {
	GeneratedAt time.Time `json:"generated_at"`
	Findings    []Finding `json:"findings"`
}

func (r Report) Healthy() bool {
	for _, finding := range r.Findings {
		if finding.Severity == SeverityError {
			return false
		}
	}
	return true
}
