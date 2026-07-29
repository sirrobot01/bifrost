package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"

	"github.com/sirrobot01/bifrost/internal/config"
	"github.com/sirrobot01/bifrost/internal/edge"
	"github.com/sirrobot01/bifrost/internal/platform"
	"github.com/sirrobot01/bifrost/internal/platformselect"
)

// edgeServiceAccount runs the edge role. It is separate from the home account
// so an edge host grants only CAP_NET_BIND_SERVICE.
const edgeServiceAccount = "bifrost-edge"

// edgeKeyBytes is the entropy behind the shared edge key. The verifier
// requires at least 32 bytes and the file is hex encoded.
const edgeKeyBytes = 32

// runEdgeInvite prepares the home side of an edge deployment and prints one
// token carrying everything the edge host needs. Transcribing a key and an
// allowlist between two machines by hand was the least approachable part of
// setting up an edge.
func (r Runner) runEdgeInvite(arguments []string) error {
	flags := flag.NewFlagSet("edge invite", flag.ContinueOnError)
	flags.SetOutput(r.Stderr)
	configPath := flags.String("config", "/etc/bifrost/config.yaml", "home config file")
	address := flags.String("address", "", "public IPv4 address of the edge host")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("edge invite accepts flags only")
	}

	configFile, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(configFile.StaticServices))
	for _, service := range configFile.StaticServices {
		names = append(names, service.DNSName)
	}
	if len(names) == 0 {
		return errors.New("this configuration publishes no static services, so there is nothing for an edge to serve")
	}

	keyPath := configFile.Edge.KeyFile
	if keyPath == "" {
		keyPath = filepath.Join(filepath.Dir(*configPath), "edge-key")
	}
	key, created, err := ensureEdgeKey(keyPath)
	if err != nil {
		return err
	}
	token, err := edge.EncodeEnrollment(edge.Enrollment{Key: key, Allow: names})
	if err != nil {
		return err
	}

	if created {
		_, _ = fmt.Fprintf(r.Stdout, "Created a shared edge key at %s.\n", keyPath)
	} else {
		_, _ = fmt.Fprintf(r.Stdout, "Reusing the existing edge key at %s.\n", keyPath)
	}
	_, _ = fmt.Fprintf(r.Stdout, "\nRun this on the edge host:\n\n  sudo bifrost edge join %s\n", token)
	_, _ = fmt.Fprint(r.Stdout, "\nThe token contains the shared key. Treat it as a secret and send it over a private channel.\n")

	_, _ = fmt.Fprintf(r.Stdout, "\nThen add this to %s and restart bifrost:\n\n", *configPath)
	_, _ = fmt.Fprint(r.Stdout, "edge:\n  enabled: true\n")
	if *address != "" {
		_, _ = fmt.Fprintf(r.Stdout, "  ipv4_address: %s\n", *address)
	} else {
		_, _ = fmt.Fprint(r.Stdout, "  ipv4_address: <public IPv4 of the edge host>\n")
	}
	_, _ = fmt.Fprintf(r.Stdout, "  key_file: %s\n", keyPath)
	_, _ = fmt.Fprint(r.Stdout, "\nand set `edge: true` on each service the edge should serve. Those services\nuse splice mode automatically.\n")
	return nil
}

// runEdgeJoin writes the edge configuration from a token, so the edge host
// needs no hand-written allowlist and no separately copied key file.
func (r Runner) runEdgeJoin(arguments []string) error {
	flags := flag.NewFlagSet("edge join", flag.ContinueOnError)
	flags.SetOutput(r.Stderr)
	configDir := flags.String("config-dir", defaultConfigDir, "directory for the edge configuration")
	start := flags.Bool("start", true, "enable and start the bifrost-edge service")
	force := flags.Bool("force", false, "overwrite an existing edge configuration")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("usage: bifrost edge join <token>")
	}

	enrollment, err := edge.DecodeEnrollment(flags.Arg(0))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(*configDir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", *configDir, err)
	}
	keyPath := filepath.Join(*configDir, "edge-key")
	configPath := filepath.Join(*configDir, "edge.yaml")
	files := []pendingFile{
		{path: keyPath, content: secretContent(enrollment.Key), mode: 0o600, label: "edge key"},
		{path: configPath, content: []byte(enrollment.ConfigYAML(keyPath)), mode: 0o600, label: "edge configuration"},
	}
	for _, file := range files {
		if err := writePendingFile(file, *force); err != nil {
			return err
		}
	}
	if err := applyAccountOwnership(edgeServiceAccount, *configDir, files); err != nil {
		return err
	}

	_, _ = fmt.Fprintf(r.Stdout, "Enrolled for %d name(s):\n", len(enrollment.Allow))
	for _, name := range enrollment.Allow {
		_, _ = fmt.Fprintf(r.Stdout, "  %s\n", name)
	}
	_, _ = fmt.Fprintf(r.Stdout, "Wrote %s and %s.\n", configPath, keyPath)

	if !*start {
		_, _ = fmt.Fprintf(r.Stdout, "\nNext: %s\n", platformselect.New().Services().StartAdvice(platform.EdgeService))
		return nil
	}
	if err := startEdgeService(); err != nil {
		_, _ = fmt.Fprintf(r.Stdout, "\nThe configuration is in place, but the service did not start: %v\nStart it with: %s\n", err, platformselect.New().Services().StartAdvice(platform.EdgeService))
		return nil
	}
	_, _ = fmt.Fprint(r.Stdout, "\nStarted bifrost-edge.\n\nOn the home host, set edge.enabled and edge.ipv4_address, mark services with\n`edge: true`, then restart bifrost so the A records are published.\n")
	return nil
}

// ensureEdgeKey returns the hex key at path, creating it when absent so that
// invite is safe to run twice.
func ensureEdgeKey(path string) (string, bool, error) {
	if existing, err := os.ReadFile(path); err == nil {
		key := string(secretContent(string(existing)))
		return key[:len(key)-1], false, nil
	}
	material := make([]byte, edgeKeyBytes)
	if _, err := rand.Read(material); err != nil {
		return "", false, err
	}
	key := hex.EncodeToString(material)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", false, err
	}
	file := pendingFile{path: path, content: secretContent(key), mode: 0o600, label: "edge key"}
	if err := writePendingFile(file, false); err != nil {
		return "", false, err
	}
	if err := applyAccountOwnership(serviceAccount, filepath.Dir(path), []pendingFile{file}); err != nil {
		return "", false, err
	}
	return key, true, nil
}

// applyAccountOwnership hands created files to a service account when running
// as root. A missing account is not an error: the operator may run the daemon
// under a different user.
func applyAccountOwnership(account, configDir string, files []pendingFile) error {
	if os.Geteuid() != 0 {
		return nil
	}
	entry, err := user.Lookup(account)
	if err != nil {
		return nil
	}
	uid, err := strconv.Atoi(entry.Uid)
	if err != nil {
		return nil
	}
	gid, err := strconv.Atoi(entry.Gid)
	if err != nil {
		return nil
	}
	for _, file := range files {
		if err := os.Chown(file.path, uid, gid); err != nil {
			return fmt.Errorf("set ownership of %s: %w", file.path, err)
		}
	}
	// The directory stays traversable by other roles: both the home and edge
	// accounts read configuration from it on a combined host.
	if err := os.Chmod(configDir, 0o755); err != nil {
		return fmt.Errorf("set mode of %s: %w", configDir, err)
	}
	return nil
}

func startEdgeService() error {
	return platformselect.New().Services().Start(context.Background(), platform.EdgeService)
}
