package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"time"

	"github.com/sirrobot01/bifrost/internal/config"
	"github.com/sirrobot01/bifrost/internal/diagnose"
	"github.com/sirrobot01/bifrost/internal/observability"
)

type Runner struct {
	Stdout  io.Writer
	Stderr  io.Writer
	Version string
}

func (r Runner) Run(ctx context.Context, arguments []string) int {
	if r.Stdout == nil {
		r.Stdout = os.Stdout
	}
	if r.Stderr == nil {
		r.Stderr = os.Stderr
	}
	if len(arguments) == 0 {
		r.usage()
		return 2
	}

	var code int
	var err error
	switch arguments[0] {
	case "init":
		err = r.runInit(arguments[1:])
	case "check":
		code, err = r.runCheck(ctx, arguments[1:])
	case "status":
		err = r.runStatus(ctx, arguments[1:])
	case "serve":
		err = r.runServe(ctx, arguments[1:])
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
	return platformServe(ctx, configFile, *dryRun, slog.New(handler), r.Stdout)
}

func (r Runner) runInit(arguments []string) error {
	flags := flag.NewFlagSet("init", flag.ContinueOnError)
	flags.SetOutput(r.Stderr)
	interfaceName := flags.String("interface", "", "publication interface; auto-detected when unambiguous")
	output := flags.String("output", "-", "config output path or - for stdout")
	force := flags.Bool("force", false, "replace an existing output file")
	ownerID := flags.String("owner-id", defaultOwnerID(), "DNS ownership identity")
	secretFile := flags.String("secret-file", "/etc/bifrost/secret", "host address-derivation secret")
	zoneID := flags.String("cloudflare-zone-id", "CHANGE_ME", "Cloudflare zone ID")
	tokenFile := flags.String("cloudflare-token-file", "/etc/bifrost/cloudflare-token", "Cloudflare token file")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("init accepts flags only")
	}
	if *interfaceName == "" {
		var err error
		*interfaceName, err = discoverInterface()
		if err != nil {
			return err
		}
	}

	configFile := config.Config{
		Version:    config.CurrentVersion,
		Interface:  *interfaceName,
		OwnerID:    *ownerID,
		SecretFile: *secretFile,
		DNS: config.DNS{
			Provider: "cloudflare",
			Cloudflare: config.Cloudflare{
				ZoneID:       *zoneID,
				APITokenFile: *tokenFile,
			},
		},
	}
	encoded, err := config.Encode(configFile)
	if err != nil {
		return err
	}
	if *output == "-" {
		_, err = r.Stdout.Write(encoded)
		return err
	}
	flagsValue := os.O_WRONLY | os.O_CREATE
	if *force {
		flagsValue |= os.O_TRUNC
	} else {
		flagsValue |= os.O_EXCL
	}
	file, err := os.OpenFile(*output, flagsValue, 0o600)
	if err != nil {
		return fmt.Errorf("create config: %w", err)
	}
	if _, err := file.Write(encoded); err != nil {
		_ = file.Close()
		return fmt.Errorf("write config: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close config: %w", err)
	}
	_, err = fmt.Fprintf(r.Stdout, "wrote %s; set DNS credentials, add a service, and create %s with mode 0600\n", *output, *secretFile)
	return err
}

func (r Runner) runCheck(ctx context.Context, arguments []string) (int, error) {
	flags := flag.NewFlagSet("check", flag.ContinueOnError)
	flags.SetOutput(r.Stderr)
	configPath := flags.String("config", "/etc/bifrost/config.yaml", "config file")
	jsonOutput := flags.Bool("json", false, "emit JSON")
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
		services = append(services, diagnose.Service{Name: service.Name, DNSName: service.DNSName, Address: service.Address, Port: service.Listen, CheckLocal: true, ClientIPPreserved: service.ClientIPPreserved})
	}
	if len(services) == 0 {
		return 0, errors.New("config has no services to check")
	}
	var prober diagnose.ExternalProber
	if configFile.Probe.Endpoint != "" {
		prober, err = diagnose.NewHTTPProber(configFile.Probe.Endpoint, nil)
		if err != nil {
			return 0, err
		}
	}
	checker := diagnose.NewChecker(nil, nil)
	report, err := checker.Check(ctx, diagnose.Input{Interface: configFile.Interface, Services: services, Probe: prober})
	if err != nil {
		return 0, err
	}
	if *jsonOutput {
		err = writeJSON(r.Stdout, report)
	} else {
		err = writeFindings(r.Stdout, report)
	}
	if err != nil {
		return 0, err
	}
	if !report.Healthy() {
		return 1, nil
	}
	return 0, nil
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
		if _, err := fmt.Fprintf(r.Stdout, "%s: %s [%s]:%d -> %s, client IP preserved: %t\n", service.Name, service.Mode, service.Address, service.Listen, service.Backend, service.ClientIPPreserved); err != nil {
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
	_, _ = fmt.Fprintln(r.Stderr, "usage: bifrost <init|check|serve|status|version> [flags]")
}

func discoverInterface() (string, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return "", fmt.Errorf("list network interfaces: %w", err)
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
	if len(eligible) == 0 {
		return "", errors.New("no interface with public IPv6 was found; pass --interface after IPv6 is configured")
	}
	if len(eligible) > 1 {
		return "", fmt.Errorf("multiple public IPv6 interfaces found (%s); pass --interface", strings.Join(eligible, ", "))
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

func writeFindings(writer io.Writer, report diagnose.Report) error {
	for _, finding := range report.Findings {
		if _, err := fmt.Fprintf(writer, "%-7s %-10s %s\n", strings.ToUpper(string(finding.Severity)), finding.Check, finding.Summary); err != nil {
			return err
		}
		if finding.Detail != "" {
			if _, err := fmt.Fprintf(writer, "                  %s\n", finding.Detail); err != nil {
				return err
			}
		}
		if finding.Remediation != "" {
			if _, err := fmt.Fprintf(writer, "                  fix: %s\n", finding.Remediation); err != nil {
				return err
			}
		}
	}
	return nil
}

func writeJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}
