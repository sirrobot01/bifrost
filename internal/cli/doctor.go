package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"strings"

	"github.com/sirrobot01/bifrost/internal/diagnose"
)

// runDoctor reports whether this host can run Bifrost. It deliberately loads no
// configuration: an operator needs this answer before they have written one,
// and requiring a config file first is what makes a failed setup hard to
// diagnose.
func (r Runner) runDoctor(ctx context.Context, arguments []string) (int, error) {
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	flags.SetOutput(r.Stderr)
	interfaceName := flags.String("interface", "", "publication interface; auto-detected when unambiguous")
	dockerSocket := flags.String("docker-socket", "", "check this Docker socket for reachability")
	offline := flags.Bool("offline", false, "skip every check that leaves the host")
	probeEndpoint := flags.String("probe", "", "HTTPS probe endpoint asked to open an inbound connection to this host")
	jsonOutput := flags.Bool("json", false, "emit JSON")
	if err := flags.Parse(arguments); err != nil {
		return 0, err
	}
	if flags.NArg() != 0 {
		return 0, errors.New("doctor accepts flags only")
	}

	var prober diagnose.ExternalProber
	if *probeEndpoint != "" && !*offline {
		httpProber, err := diagnose.NewHTTPProber(*probeEndpoint, nil)
		if err != nil {
			return 0, err
		}
		prober = httpProber
	}
	report, err := r.preflight(ctx, *interfaceName, *dockerSocket, *offline, prober)
	if err != nil {
		return 0, err
	}
	if *jsonOutput {
		if err := writeJSON(r.Stdout, report); err != nil {
			return 0, err
		}
	} else {
		if err := writeFindings(r.Stdout, report); err != nil {
			return 0, err
		}
		if err := writeDoctorSummary(r.Stdout, report); err != nil {
			return 0, err
		}
	}
	if !report.Healthy() {
		return 1, nil
	}
	return 0, nil
}

func (r Runner) preflight(ctx context.Context, interfaceName, dockerSocket string, offline bool, prober diagnose.ExternalProber) (diagnose.Report, error) {
	if interfaceName == "" {
		eligible, err := eligibleInterfaces()
		if err != nil {
			return diagnose.Report{}, err
		}
		switch len(eligible) {
		case 0:
			return blockedReport(diagnose.Finding{
				Check:       "interface",
				Severity:    diagnose.SeverityError,
				Summary:     "no interface carries a public IPv6 address",
				Remediation: "confirm the ISP delegates a routed IPv6 prefix and this host receives a global address, then re-run bifrost doctor",
			}), nil
		case 1:
			interfaceName = eligible[0]
		default:
			return blockedReport(diagnose.Finding{
				Check:       "interface",
				Severity:    diagnose.SeverityError,
				Summary:     "several interfaces carry a public IPv6 address",
				Detail:      "found " + strings.Join(eligible, ", "),
				Remediation: "re-run with --interface to name the one that should publish services",
			}), nil
		}
	}

	snapshot, err := platformSnapshot(interfaceName)
	if err != nil {
		return blockedReport(diagnose.Finding{
			Check:       "interface",
			Severity:    diagnose.SeverityError,
			Summary:     "publication interface could not be inspected",
			Detail:      err.Error(),
			Remediation: "pass --interface with the name of an interface that holds a public IPv6 address",
		}), nil
	}

	// Constructed only once an interface is settled, because building the
	// default auditor opens a netlink socket.
	checker := diagnose.NewChecker(nil, nil)
	return checker.Preflight(ctx, diagnose.PreflightInput{
		Interface:    interfaceName,
		MTU:          snapshot.MTU,
		Candidates:   snapshot.Candidates,
		Privileged:   os.Geteuid() == 0,
		DockerSocket: dockerSocket,
		Probe:        prober,
		SkipNetwork:  offline,
	})
}

// blockedReport wraps the single finding that stopped preflight before the host
// could be inspected. It is still a Report so that --json callers always receive
// the same shape.
func blockedReport(finding diagnose.Finding) diagnose.Report {
	return diagnose.Report{Findings: []diagnose.Finding{finding}}
}

func writeDoctorSummary(writer io.Writer, report diagnose.Report) error {
	errorCount, warningCount := severityCounts(report)
	if errorCount > 0 {
		summary := fmt.Sprintf("\n%s must be fixed before Bifrost can publish a service", pluralize(errorCount, "problem"))
		if warningCount > 0 {
			summary += fmt.Sprintf(", and %s to review", pluralize(warningCount, "warning"))
		}
		_, err := fmt.Fprintln(writer, summary+".")
		return err
	}
	if warningCount > 0 {
		_, err := fmt.Fprintf(writer, "\nThis host can run Bifrost, with %s to review.\nNext: bifrost init --interactive\n",
			pluralize(warningCount, "warning"))
		return err
	}
	_, err := fmt.Fprint(writer, "\nThis host can run Bifrost.\nNext: bifrost init --interactive\n")
	return err
}

func pluralize(count int, noun string) string {
	if count == 1 {
		return fmt.Sprintf("1 %s", noun)
	}
	return fmt.Sprintf("%d %ss", count, noun)
}

// eligibleInterfaces returns every interface holding a public IPv6 address.
func eligibleInterfaces() ([]string, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("list network interfaces: %w", err)
	}
	var eligible []string
	for _, networkInterface := range interfaces {
		addresses, err := networkInterface.Addrs()
		if err != nil {
			continue
		}
		for _, address := range addresses {
			prefix, err := netip.ParsePrefix(address.String())
			if err == nil && prefix.Addr().Is6() && prefix.Addr().IsGlobalUnicast() && !prefix.Addr().IsPrivate() {
				eligible = append(eligible, networkInterface.Name)
				break
			}
		}
	}
	return eligible, nil
}
