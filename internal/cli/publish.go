package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"

	"github.com/sirrobot01/bifrost/internal/config"
	"github.com/sirrobot01/bifrost/internal/platform"
)

func (r Runner) runPublish(ctx context.Context, arguments []string) error {
	flags := flag.NewFlagSet("publish", flag.ContinueOnError)
	flags.SetOutput(r.Stderr)
	configPath := flags.String("config", "/etc/bifrost/config.yaml", "config file")
	name := flags.String("name", "", "service ID (default: the first label of the DNS name)")
	listen := flags.Uint("listen", 443, "public TCP port")
	tls := flags.String("tls", "auto", "auto to terminate TLS, off for a backend that speaks TLS itself")
	edge := flags.Bool("edge", false, "also publish through the configured IPv4 edge")
	dryRun := flags.Bool("dry-run", false, "print the service block without writing it")
	noReload := flags.Bool("no-reload", false, "do not reload the running daemon")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 2 {
		return errors.New("usage: bifrost publish <dns-name> <backend-address:port>")
	}
	dnsName := strings.ToLower(strings.TrimSuffix(flags.Arg(0), "."))
	backend := flags.Arg(1)
	if _, err := netip.ParseAddrPort(backend); err != nil {
		return fmt.Errorf("backend must be an IP address and port, for example 127.0.0.1:8096: %w", err)
	}
	if *listen == 0 || *listen > 65535 {
		return errors.New("listen must be a TCP port between 1 and 65535")
	}
	if *tls != "auto" && *tls != "off" {
		return errors.New("tls must be auto or off")
	}
	service := config.StaticService{
		Name:    cmpOr(*name, serviceNameFromDNS(dnsName)),
		Backend: backend,
		Listen:  uint16(*listen),
		DNSName: dnsName,
		Edge:    *edge,
	}
	if *tls == "off" {
		service.TLS = "off"
	}

	original, err := os.ReadFile(*configPath)
	if err != nil {
		return err
	}
	current, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	for _, existing := range current.StaticServices {
		if existing.Name == service.Name {
			return fmt.Errorf("a service named %q already exists; pass --name to choose another", service.Name)
		}
		if existing.DNSName == service.DNSName {
			return fmt.Errorf("%s is already published by service %q", service.DNSName, existing.Name)
		}
	}

	updated, err := appendService(string(original), service)
	if err != nil {
		return err
	}
	if *dryRun {
		_, err := fmt.Fprint(r.Stdout, serviceBlock(service))
		return err
	}
	if err := writeConfigIfValid(*configPath, updated); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(r.Stdout, "Added %s to %s.\n", service.Name, *configPath); err != nil {
		return err
	}
	if *noReload {
		return nil
	}
	return r.reloadAfterPublish(ctx)
}

// cmpOr returns the first non-empty value. It exists so the flag default can be
// empty and still mean "derive it".
func cmpOr(preferred, fallback string) string {
	if preferred != "" {
		return preferred
	}
	return fallback
}

func serviceBlock(service config.StaticService) string {
	var block strings.Builder
	fmt.Fprintf(&block, "  - name: %s\n", service.Name)
	fmt.Fprintf(&block, "    backend: %s\n", service.Backend)
	fmt.Fprintf(&block, "    listen: %d\n", service.Listen)
	fmt.Fprintf(&block, "    dns: %s\n", service.DNSName)
	if service.TLS != "" {
		fmt.Fprintf(&block, "    tls: %s\n", service.TLS)
	}
	if service.Edge {
		block.WriteString("    edge: true\n")
	}
	return block.String()
}

// appendService inserts a service into the existing file text rather than
// re-marshalling the configuration, because a round trip through the struct
// would silently delete every comment and reorder the file an operator wrote.
func appendService(original string, service config.StaticService) (string, error) {
	lines := strings.Split(original, "\n")
	start := -1
	for index, line := range lines {
		trimmed := strings.TrimRight(line, " \t")
		if trimmed == "static_services:" || trimmed == "static_services: []" {
			start = index
			break
		}
		// A key at column zero is the only form this understands. Anything else
		// is reported rather than guessed at.
		if strings.HasPrefix(line, "static_services:") {
			return "", fmt.Errorf("static_services in this file is not a form publish can edit: %q", strings.TrimSpace(line))
		}
	}
	if start == -1 {
		return "", errors.New("this file has no top-level static_services key; add the service by hand")
	}
	if strings.TrimRight(lines[start], " \t") == "static_services: []" {
		lines[start] = "static_services:"
	}
	// The block ends at the next line that starts a top-level key.
	end := len(lines)
	for index := start + 1; index < len(lines); index++ {
		line := lines[index]
		if line == "" || strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") || strings.HasPrefix(line, "#") {
			continue
		}
		end = index
		break
	}
	// Trailing blank lines belong after the insertion, not before it.
	for end > start+1 && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}

	block := strings.Split(strings.TrimRight(serviceBlock(service), "\n"), "\n")
	updated := make([]string, 0, len(lines)+len(block))
	updated = append(updated, lines[:end]...)
	updated = append(updated, block...)
	updated = append(updated, lines[end:]...)
	return strings.Join(updated, "\n"), nil
}

// writeConfigIfValid parses the result before it replaces the file, so a
// malformed edit never reaches a path the daemon reads.
func writeConfigIfValid(path, contents string) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".bifrost-config-*")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err := temporary.WriteString(contents); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if _, err := config.Load(name); err != nil {
		return fmt.Errorf("the edited configuration is not valid, so nothing was written: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if err := os.Chmod(name, info.Mode().Perm()); err != nil {
		return err
	}
	if err := matchConfigOwnership(name, info); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// reloadAfterPublish applies the change to a running daemon. A host that is not
// running one is told what to do rather than treated as an error, because
// publishing before the first start is a normal order to work in.
func (r Runner) reloadAfterPublish(ctx context.Context) error {
	services := platform.New().Services()
	if !slicesContains(runningUnits(services), platform.HomeService) {
		_, err := fmt.Fprintln(r.Stdout, "bifrost is not running. Start it with: "+services.StartAdvice(platform.HomeService))
		return err
	}
	if err := services.Reload(ctx, platform.HomeService); err != nil {
		return fmt.Errorf("reload bifrost: %w", err)
	}
	_, err := fmt.Fprintln(r.Stdout, "Reloaded bifrost. Confirm with: sudo bifrost check")
	return err
}

func slicesContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
