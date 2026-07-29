package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"time"

	"github.com/sirrobot01/bifrost/internal/config"
	"github.com/sirrobot01/bifrost/internal/diagnose"
	"github.com/sirrobot01/bifrost/internal/dnspublish"
	"github.com/sirrobot01/bifrost/internal/edge"
	"github.com/sirrobot01/bifrost/internal/observability"
)

type Runner struct {
	Stdout io.Writer
	Stderr io.Writer
	// Stdin answers interactive prompts. It is an *os.File because hiding
	// typed secrets needs the underlying terminal descriptor.
	Stdin   *os.File
	Version string
	// zoneLookup overrides provider zone discovery during init. Tests set it
	// to avoid real API calls; nil selects the provider implementations.
	zoneLookup zoneLookupFunc
}

func (r Runner) Run(ctx context.Context, arguments []string) int {
	if r.Stdout == nil {
		r.Stdout = os.Stdout
	}
	if r.Stderr == nil {
		r.Stderr = os.Stderr
	}
	if r.Stdin == nil {
		r.Stdin = os.Stdin
	}
	if len(arguments) == 0 {
		r.usage()
		return 2
	}

	var code int
	var err error
	switch arguments[0] {
	case "init":
		err = r.runInit(ctx, arguments[1:])
	case "doctor":
		code, err = r.runDoctor(ctx, arguments[1:])
	case "check":
		code, err = r.runCheck(ctx, arguments[1:])
	case "status":
		err = r.runStatus(ctx, arguments[1:])
	case "serve":
		err = r.runServe(ctx, arguments[1:])
	case "edge":
		err = r.runEdge(ctx, arguments[1:])
	case "upgrade":
		err = r.runUpgrade(ctx, arguments[1:])
	case "version":
		_, err = fmt.Fprintln(r.Stdout, r.Version)
	case "help", "-h", "--help":
		r.usage()
	default:
		err = fmt.Errorf("unknown command %q", arguments[0])
	}
	if err != nil {
		_, _ = fmt.Fprintf(r.Stderr, "bifrost %s: %v\n", arguments[0], err)
		return 1
	}
	return code
}

func (r Runner) runEdge(ctx context.Context, arguments []string) error {
	// Enrollment subcommands come before the server flags so that `edge join`
	// reads as a verb rather than an argument to the server.
	if len(arguments) > 0 {
		switch arguments[0] {
		case "invite":
			return r.runEdgeInvite(arguments[1:])
		case "join":
			return r.runEdgeJoin(arguments[1:])
		}
	}
	flags := flag.NewFlagSet("edge", flag.ContinueOnError)
	flags.SetOutput(r.Stderr)
	configPath := flags.String("config", "/etc/bifrost/edge.yaml", "edge config file")
	logFormat := flags.String("log-format", "json", "json or text")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("edge accepts flags only")
	}
	var handler slog.Handler
	switch *logFormat {
	case "json":
		handler = slog.NewJSONHandler(r.Stderr, nil)
	case "text":
		handler = slog.NewTextHandler(r.Stderr, nil)
	default:
		return errors.New("log-format must be json or text")
	}
	configFile, err := edge.LoadConfig(*configPath)
	if err != nil {
		return err
	}
	server, err := edge.NewServer(configFile, nil, slog.New(handler))
	if err != nil {
		return err
	}
	return server.Run(ctx)
}

func (r Runner) runServe(ctx context.Context, arguments []string) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	flags.SetOutput(r.Stderr)
	configPath := flags.String("config", "/etc/bifrost/config.yaml", "config file")
	dryRun := flags.Bool("dry-run", false, "print the reconciliation plan without mutations")
	logFormat := flags.String("log-format", "json", "json or text")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("serve accepts flags only")
	}
	var handler slog.Handler
	switch *logFormat {
	case "json":
		handler = slog.NewJSONHandler(r.Stderr, nil)
	case "text":
		handler = slog.NewTextHandler(r.Stderr, nil)
	default:
		return errors.New("log-format must be json or text")
	}
	configFile, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	return platformServe(ctx, *configPath, configFile, *dryRun, slog.New(handler), r.Stdout)
}

func (r Runner) runCheck(ctx context.Context, arguments []string) (int, error) {
	flags := flag.NewFlagSet("check", flag.ContinueOnError)
	flags.SetOutput(r.Stderr)
	configPath := flags.String("config", "/etc/bifrost/config.yaml", "config file")
	jsonOutput := flags.Bool("json", false, "emit JSON")
	requireExternal := flags.Bool("require-external", false, "fail unless the services answered from outside this network")
	if err := flags.Parse(arguments); err != nil {
		return 0, err
	}
	if flags.NArg() != 0 {
		return 0, errors.New("check accepts flags only")
	}
	configFile, err := config.Load(*configPath)
	if err != nil {
		return 0, err
	}
	resolved, err := resolve(configFile)
	if err != nil {
		return 0, err
	}
	services := make([]diagnose.Service, 0, len(resolved.Services))
	for _, service := range resolved.Services {
		services = append(services, diagnose.Service{Name: service.Name, DNSName: service.DNSName, Address: service.Address, Port: service.Listen, CheckLocal: true, ClientIPPreserved: service.ClientIPPreserved, TLS: service.TerminatesTLS, EdgeAddresses: service.EdgeAddresses})
	}
	if len(services) == 0 {
		return 0, errors.New("config has no services to check")
	}
	prober, err := externalProber(configFile)
	if err != nil {
		return 0, err
	}
	checker := diagnose.NewChecker(nil, nil)
	report, err := checker.Check(ctx, diagnose.Input{Interface: configFile.Interface, Services: services, Probe: prober})
	if err != nil {
		return 0, err
	}
	provider, err := dnspublish.NewProvider(configFile.DNS)
	if err != nil {
		report.Findings = append(report.Findings, diagnose.Finding{Check: "dns-owner", Severity: diagnose.SeverityError, Summary: "DNS provider credentials or configuration are unavailable", Detail: err.Error(), Remediation: "install the provider credential with mode 0600 and verify the configured zone"})
	} else {
		reconciler, err := dnspublish.NewReconciler(provider, configFile.OwnerID)
		if err != nil {
			return 0, err
		}
		for _, service := range resolved.Services {
			err := reconciler.Verify(ctx, dnspublish.Publication{Name: service.DNSName, Addresses: []netip.Addr{service.Address}, EdgeAddresses: service.EdgeAddresses, TTL: configFile.DNS.TTL.Duration()})
			if err != nil {
				report.Findings = append(report.Findings, diagnose.Finding{Check: "dns-owner", Severity: diagnose.SeverityError, Service: service.Name, Summary: "provider DNS state or ownership is incorrect", Detail: err.Error(), Remediation: "run bifrost serve --dry-run, then reconcile only after reviewing the planned DNS changes"})
			} else {
				report.Findings = append(report.Findings, diagnose.Finding{Check: "dns-owner", Severity: diagnose.SeverityInfo, Service: service.Name, Summary: "provider DNS state is owned and matches the configuration"})
			}
		}
	}
	if *jsonOutput {
		err = writeJSON(r.Stdout, report)
	} else {
		err = writeFindings(r.Stdout, report)
		if err == nil {
			err = writeCheckSummary(r.Stdout, report)
		}
	}
	if err != nil {
		return 0, err
	}
	if !report.Healthy() {
		return 1, nil
	}
	if *requireExternal && report.Verification != diagnose.VerificationExternal {
		_, _ = fmt.Fprintln(r.Stderr, "bifrost check: --require-external was set but not every service was confirmed from outside this host")
		return 1, nil
	}
	return 0, nil
}

// externalProber selects how reachability is confirmed from outside the host.
// A configured endpoint wins; otherwise a configured edge is used, because an
// edge already sits outside the network and reaching a service through it
// exercises the same inbound path a real client takes. Only a deployment with
// neither goes unverified.
func externalProber(configFile config.Config) (diagnose.ExternalProber, error) {
	if configFile.Probe.Endpoint != "" {
		return diagnose.NewHTTPProber(configFile.Probe.Endpoint, nil)
	}
	if configFile.Edge.Enabled && len(configFile.Edge.IPv4Addresses) > 0 {
		addresses, err := edgeIPv4Addresses(configFile)
		if err != nil {
			return nil, err
		}
		return diagnose.NewEdgeProber(addresses...), nil
	}
	return nil, nil
}

func (r Runner) runStatus(ctx context.Context, arguments []string) error {
	flags := flag.NewFlagSet("status", flag.ContinueOnError)
	flags.SetOutput(r.Stderr)
	configPath := flags.String("config", "/etc/bifrost/config.yaml", "config file")
	jsonOutput := flags.Bool("json", false, "emit JSON")
	offline := flags.Bool("offline", false, "derive desired state without contacting the daemon")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("status accepts flags only")
	}
	configFile, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	if !*offline {
		return r.writeLiveStatus(ctx, configFile, *jsonOutput)
	}
	status, err := resolve(configFile)
	if err != nil {
		return err
	}
	if *jsonOutput {
		return writeJSON(r.Stdout, status)
	}
	_, err = fmt.Fprintf(r.Stdout, "interface: %s (MTU %d)\n", status.Interface, status.MTU)
	if err != nil {
		return err
	}
	if status.SelectedPrefix.IsValid() {
		if _, err := fmt.Fprintf(r.Stdout, "selected prefix: %s\n", status.SelectedPrefix); err != nil {
			return err
		}
	}
	for _, prefix := range status.IgnoredPrefixes {
		if _, err := fmt.Fprintf(r.Stdout, "ignored prefix: %s\n", prefix); err != nil {
			return err
		}
	}
	for _, service := range status.Services {
		edge := "disabled"
		if len(service.EdgeAddresses) > 0 {
			parts := make([]string, 0, len(service.EdgeAddresses))
			for _, address := range service.EdgeAddresses {
				parts = append(parts, address.String())
			}
			edge = strings.Join(parts, ",")
		}
		if _, err := fmt.Fprintf(r.Stdout, "%s: %s [%s]:%d -> %s, edge: %s, client IP preserved: %t\n", service.Name, service.Mode, service.Address, service.Listen, service.Backend, edge, service.ClientIPPreserved); err != nil {
			return err
		}
	}
	return nil
}

func (r Runner) writeLiveStatus(ctx context.Context, configFile config.Config, jsonOutput bool) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+configFile.Metrics.Listen+"/status", nil)
	if err != nil {
		return err
	}
	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
	if err != nil {
		return fmt.Errorf("query running daemon: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("daemon status returned HTTP %d", response.StatusCode)
	}
	var status observability.Snapshot
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&status); err != nil {
		return fmt.Errorf("decode daemon status: %w", err)
	}
	if jsonOutput {
		return writeJSON(r.Stdout, status)
	}
	if _, err := fmt.Fprintf(r.Stdout, "ready: %t\n", status.Ready); err != nil {
		return err
	}
	if status.LastError != "" {
		if _, err := fmt.Fprintf(r.Stdout, "last error: %s\n", status.LastError); err != nil {
			return err
		}
	}
	for _, service := range status.Services {
		if _, err := fmt.Fprintf(r.Stdout, "%s: %s %v -> %s, connections: %d, client IP preserved: %t\n", service.ID, service.Mode, service.Addresses, service.Backend, service.ActiveConnections, service.ClientIPPreserved); err != nil {
			return err
		}
	}
	return nil
}

func (r Runner) usage() {
	_, _ = fmt.Fprintln(r.Stderr, "usage: bifrost <doctor|init|check|edge|serve|status|upgrade|version> [flags]")
	_, _ = fmt.Fprintln(r.Stderr)
	_, _ = fmt.Fprintln(r.Stderr, "  doctor   report whether this host can run Bifrost; needs no config")
	_, _ = fmt.Fprintln(r.Stderr, "  init     create the configuration, secret, and credential files")
	_, _ = fmt.Fprintln(r.Stderr, "  check    diagnose a configured deployment end to end")
	_, _ = fmt.Fprintln(r.Stderr, "  serve    run the home publication daemon")
	_, _ = fmt.Fprintln(r.Stderr, "  edge     run the optional IPv4 edge")
	_, _ = fmt.Fprintln(r.Stderr, "           edge invite   print one token enrolling an edge host (run at home)")
	_, _ = fmt.Fprintln(r.Stderr, "           edge join     configure this host from a token (run on the edge)")
	_, _ = fmt.Fprintln(r.Stderr, "  status   show desired or running state")
	_, _ = fmt.Fprintln(r.Stderr, "  upgrade  replace this binary with the latest release")
	_, _ = fmt.Fprintln(r.Stderr, "  version  print the build version")
}

func discoverInterface() (string, error) {
	eligible, err := eligibleInterfaces()
	if err != nil {
		return "", err
	}
	if len(eligible) == 0 {
		return "", errors.New("no interface carries a public IPv6 address; run bifrost doctor for the specific reason, then pass --interface")
	}
	if len(eligible) > 1 {
		return "", fmt.Errorf("several interfaces carry a public IPv6 address (%s); pass --interface to name the publication interface", strings.Join(eligible, ", "))
	}
	return eligible[0], nil
}

func defaultOwnerID() string {
	hostname, err := os.Hostname()
	if err != nil {
		return "bifrost-home"
	}
	hostname = strings.ToLower(hostname)
	var result strings.Builder
	result.WriteString("bifrost-")
	for _, character := range hostname {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '-' || character == '_' || character == '.' {
			result.WriteRune(character)
		} else {
			result.WriteByte('-')
		}
	}
	return result.String()
}

func writeJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}
