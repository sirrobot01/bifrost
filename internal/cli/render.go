package cli

import (
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/sirrobot01/bifrost/internal/diagnose"
)

// troubleshootingURL explains every doctor and check finding. The anchor for a
// finding is its check slug, which the headings on that page mirror.
const troubleshootingURL = "https://bifrost.biodun.dev/getting-started/troubleshooting/"

// findingIndent aligns continuation lines under the summary column produced by
// the severity and check widths in writeFinding.
const findingIndent = "                    "

// palette holds the escape sequences for one output mode. The zero value
// renders plain text, which keeps piped and redirected output identical to the
// format scripts already consume.
type palette struct {
	red, yellow, green, cyan, dim, reset string
}

func (p palette) enabled() bool {
	return p.reset != ""
}

var ansiPalette = palette{
	red:    "\x1b[31m",
	yellow: "\x1b[33m",
	green:  "\x1b[32m",
	cyan:   "\x1b[36m",
	dim:    "\x1b[2m",
	reset:  "\x1b[0m",
}

// paletteFor enables color only for an interactive terminal that has not opted
// out through NO_COLOR (https://no-color.org) or TERM=dumb.
func paletteFor(writer io.Writer) palette {
	file, ok := writer.(*os.File)
	if !ok {
		return palette{}
	}
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return palette{}
	}
	info, err := file.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return palette{}
	}
	return ansiPalette
}

func severityStyle(colors palette, severity diagnose.Severity) (glyph, tone string) {
	switch severity {
	case diagnose.SeverityError:
		return "✗", colors.red
	case diagnose.SeverityWarning:
		return "!", colors.yellow
	default:
		return "✓", colors.green
	}
}

func writeFindings(writer io.Writer, report diagnose.Report) error {
	colors := paletteFor(writer)
	for _, finding := range report.Findings {
		if finding.Service != "" {
			return writeServiceReport(writer, colors, report)
		}
	}
	for _, finding := range report.Findings {
		if err := writeFinding(writer, colors, finding); err != nil {
			return err
		}
	}
	return nil
}

// writeServiceReport renders a check report: host findings in full, then one
// summary line per service, then full detail for errors only, then warnings
// grouped across services. Per-service INFO lines appear only in the summary
// line; their detail stays available in --json.
func writeServiceReport(writer io.Writer, colors palette, report diagnose.Report) error {
	var host []diagnose.Finding
	perService := make(map[string][]diagnose.Finding)
	var serviceOrder []string
	for _, finding := range report.Findings {
		if finding.Service == "" {
			host = append(host, finding)
			continue
		}
		if _, seen := perService[finding.Service]; !seen {
			serviceOrder = append(serviceOrder, finding.Service)
		}
		perService[finding.Service] = append(perService[finding.Service], finding)
	}
	slices.Sort(serviceOrder)

	for _, finding := range host {
		if err := writeFinding(writer, colors, finding); err != nil {
			return err
		}
	}

	nameWidth := 0
	for _, name := range serviceOrder {
		nameWidth = max(nameWidth, len(name))
	}
	if _, err := fmt.Fprintln(writer); err != nil {
		return err
	}
	for _, name := range serviceOrder {
		if err := writeServiceLine(writer, colors, name, nameWidth, perService[name]); err != nil {
			return err
		}
	}

	var errorDetails []diagnose.Finding
	for _, name := range serviceOrder {
		for _, finding := range perService[name] {
			if finding.Severity == diagnose.SeverityError {
				detailed := finding
				detailed.Summary = finding.Service + ": " + finding.Summary
				errorDetails = append(errorDetails, detailed)
			}
		}
	}
	if len(errorDetails) > 0 {
		if _, err := fmt.Fprintln(writer); err != nil {
			return err
		}
		for _, finding := range errorDetails {
			if err := writeFinding(writer, colors, finding); err != nil {
				return err
			}
		}
	}

	groups, order := groupWarnings(report.Findings)
	if len(order) > 0 {
		if _, err := fmt.Fprintln(writer); err != nil {
			return err
		}
		for _, key := range order {
			group := groups[key]
			grouped := group.finding
			services := "services: " + strings.Join(group.services, ", ")
			if grouped.Detail == "" {
				grouped.Detail = services
			} else {
				grouped.Detail += "; " + services
			}
			if err := writeFinding(writer, colors, grouped); err != nil {
				return err
			}
		}
	}
	return nil
}

// writeServiceLine renders one service as a single line of grouped check
// names: passing checks first, then warnings, then failures.
func writeServiceLine(writer io.Writer, colors palette, name string, nameWidth int, findings []diagnose.Finding) error {
	buckets := map[diagnose.Severity][]string{}
	for _, finding := range findings {
		buckets[finding.Severity] = append(buckets[finding.Severity], finding.Check)
	}
	var segments []string
	for _, class := range []struct {
		severity diagnose.Severity
		label    string
		tone     func(palette) string
	}{
		{diagnose.SeverityInfo, "ok:", func(p palette) string { return p.green }},
		{diagnose.SeverityWarning, "warn:", func(p palette) string { return p.yellow }},
		{diagnose.SeverityError, "FAIL:", func(p palette) string { return p.red }},
	} {
		checks := buckets[class.severity]
		if len(checks) == 0 {
			continue
		}
		label := class.label
		if colors.enabled() {
			label = class.tone(colors) + label + colors.reset
		}
		segments = append(segments, label+" "+strings.Join(checks, " "))
	}
	_, err := fmt.Fprintf(writer, "%-*s  %s\n", nameWidth, name, strings.Join(segments, "  "))
	return err
}

type warningGroup struct {
	finding  diagnose.Finding
	services []string
}

// groupWarnings collapses identical warnings that differ only by service, so
// a fleet of splice services reports "splice mode hides the client address"
// once instead of once per service.
func groupWarnings(findings []diagnose.Finding) (map[string]*warningGroup, []string) {
	groups := make(map[string]*warningGroup)
	var order []string
	for _, finding := range findings {
		if finding.Service == "" || finding.Severity != diagnose.SeverityWarning {
			continue
		}
		key := finding.Check + "\x00" + finding.Summary
		group, seen := groups[key]
		if !seen {
			template := finding
			template.Service = ""
			template.Detail = ""
			groups[key] = &warningGroup{finding: template, services: []string{finding.Service}}
			order = append(order, key)
			continue
		}
		group.services = append(group.services, finding.Service)
	}
	return groups, order
}

func writeFinding(writer io.Writer, colors palette, finding diagnose.Finding) error {
	severity := fmt.Sprintf("%-7s", strings.ToUpper(string(finding.Severity)))
	check := fmt.Sprintf("%-11s", finding.Check)
	indent := findingIndent
	if colors.enabled() {
		glyph, tone := severityStyle(colors, finding.Severity)
		severity = tone + glyph + " " + severity + colors.reset
		check = colors.dim + check + colors.reset
		// The glyph and its space widen the first column by two characters.
		indent += "  "
	}
	if _, err := fmt.Fprintf(writer, "%s %s %s\n", severity, check, finding.Summary); err != nil {
		return err
	}
	if finding.Detail != "" {
		if _, err := fmt.Fprintf(writer, "%s%s\n", indent, finding.Detail); err != nil {
			return err
		}
	}
	if finding.Remediation != "" {
		label := "fix:"
		if colors.enabled() {
			label = colors.cyan + label + colors.reset
		}
		if _, err := fmt.Fprintf(writer, "%s%s %s\n", indent, label, finding.Remediation); err != nil {
			return err
		}
	}
	if finding.Severity != diagnose.SeverityInfo {
		docs := fmt.Sprintf("docs: %s#%s", troubleshootingURL, finding.Check)
		if colors.enabled() {
			docs = colors.dim + docs + colors.reset
		}
		if _, err := fmt.Fprintf(writer, "%s%s\n", indent, docs); err != nil {
			return err
		}
	}
	return nil
}

// severityCounts reports raw error counts and deduplicated warning counts:
// the same warning across N services is one thing to review, not N.
func severityCounts(report diagnose.Report) (errors, warnings int) {
	warningKeys := make(map[string]struct{})
	for _, finding := range report.Findings {
		switch finding.Severity {
		case diagnose.SeverityError:
			errors++
		case diagnose.SeverityWarning:
			warningKeys[finding.Check+"\x00"+finding.Summary] = struct{}{}
		}
	}
	return errors, len(warningKeys)
}

// writeCheckSummary closes a check run with a verdict, mirroring the one
// doctor prints, so a report never ends on a bare finding list.
func writeCheckSummary(writer io.Writer, report diagnose.Report) error {
	errorCount, warningCount := severityCounts(report)
	if errorCount > 0 {
		summary := fmt.Sprintf("\n%s block the published path", pluralize(errorCount, "problem"))
		if errorCount == 1 {
			summary = "\n1 problem blocks the published path"
		}
		if warningCount > 0 {
			summary += fmt.Sprintf(", and %s to review", pluralize(warningCount, "warning"))
		}
		_, err := fmt.Fprintln(writer, summary+".")
		return err
	}
	if warningCount > 0 {
		_, err := fmt.Fprintf(writer, "\nAll checks passed, with %s to review.\n", pluralize(warningCount, "warning"))
		return err
	}
	_, err := fmt.Fprint(writer, "\nAll checks passed.\n")
	return err
}
