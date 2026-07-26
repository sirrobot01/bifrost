package cli

import (
	"fmt"
	"io"
	"os"
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
		if err := writeFinding(writer, colors, finding); err != nil {
			return err
		}
	}
	return nil
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

func severityCounts(report diagnose.Report) (errors, warnings int) {
	for _, finding := range report.Findings {
		switch finding.Severity {
		case diagnose.SeverityError:
			errors++
		case diagnose.SeverityWarning:
			warnings++
		}
	}
	return errors, warnings
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
