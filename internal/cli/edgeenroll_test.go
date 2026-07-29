package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sirrobot01/bifrost/internal/config"
	"github.com/sirrobot01/bifrost/internal/edge"
)

const enrollHomeConfig = `version: 1
interface: eth0
owner_id: home-1
secret_file: /etc/bifrost/address-secret
settle_window: 10s
drain_grace: 15s
dns:
  provider: desec
  ttl: 3600s
  desec:
    zone: example.com
    token_file: /etc/bifrost/desec-token
firewall:
  mode: advisory
metrics:
  listen: 127.0.0.1:9098
static_services:
  - name: plex
    backend: 127.0.0.1:32400
    listen: 443
    dns: plex.example.com
    mode: splice
  - name: emby
    backend: 127.0.0.1:8096
    listen: 443
    dns: emby.example.com
    mode: splice
`

// TestEdgeInviteThenJoin walks the whole enrollment: the home host prints one
// token, and the edge host turns it into a working configuration without the
// operator transcribing a key or an allowlist.
func TestEdgeInviteThenJoin(t *testing.T) {
	homeDir := t.TempDir()
	configPath := filepath.Join(homeDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(enrollHomeConfig), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	runner := Runner{Stdout: &stdout, Stderr: &stderr, Version: "test"}
	if code := runner.Run(t.Context(), []string{"edge", "invite", "--config", configPath, "--address", "192.0.2.10"}); code != 0 {
		t.Fatalf("invite failed: %s", stderr.String())
	}
	output := stdout.String()
	if !strings.Contains(output, "ipv4_address: 192.0.2.10") {
		t.Fatalf("invite did not echo the edge address:\n%s", output)
	}

	token := extractToken(t, output)
	enrollment, err := edge.DecodeEnrollment(token)
	if err != nil {
		t.Fatalf("invite printed an undecodable token: %v", err)
	}
	// The allowlist comes from the home configuration, so the operator never
	// writes the names twice.
	if len(enrollment.Allow) != 2 || enrollment.Allow[0] != "plex.example.com" {
		t.Fatalf("allow = %v", enrollment.Allow)
	}
	keyBytes, err := os.ReadFile(filepath.Join(homeDir, "edge-key"))
	if err != nil {
		t.Fatalf("invite did not create the home key: %v", err)
	}
	if strings.TrimSpace(string(keyBytes)) != enrollment.Key {
		t.Fatal("the token key differs from the key written at home")
	}

	// Running invite again must not rotate the key, or an already-enrolled
	// edge would stop verifying.
	stdout.Reset()
	if code := runner.Run(t.Context(), []string{"edge", "invite", "--config", configPath}); code != 0 {
		t.Fatalf("second invite failed: %s", stderr.String())
	}
	second, err := edge.DecodeEnrollment(extractToken(t, stdout.String()))
	if err != nil {
		t.Fatal(err)
	}
	if second.Key != enrollment.Key {
		t.Fatal("a second invite rotated the shared key")
	}

	edgeDir := t.TempDir()
	stdout.Reset()
	if code := runner.Run(t.Context(), []string{"edge", "join", "--config-dir", edgeDir, "--start=false", token}); code != 0 {
		t.Fatalf("join failed: %s", stderr.String())
	}

	configFile, err := edge.LoadConfig(filepath.Join(edgeDir, "edge.yaml"))
	if err != nil {
		t.Fatalf("join wrote an edge config that does not load: %v", err)
	}
	if len(configFile.Allow) != 2 {
		t.Fatalf("allow = %v", configFile.Allow)
	}
	written, err := os.ReadFile(filepath.Join(edgeDir, "edge-key"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(written)) != enrollment.Key {
		t.Fatal("the edge key does not match the token")
	}
	info, err := os.Stat(filepath.Join(edgeDir, "edge-key"))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("edge key mode = %04o, want 0600", info.Mode().Perm())
	}
	if _, err := config.ReadSecret(filepath.Join(edgeDir, "edge-key")); err != nil {
		t.Fatalf("edge key permissions: %v", err)
	}
}

func TestEdgeJoinRejectsGarbage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	runner := Runner{Stdout: &stdout, Stderr: &stderr, Version: "test"}
	if code := runner.Run(t.Context(), []string{"edge", "join", "--config-dir", t.TempDir(), "--start=false", "not-a-token"}); code == 0 {
		t.Fatal("join accepted a token that is not one")
	}
}

func extractToken(t *testing.T, output string) string {
	t.Helper()
	for _, field := range strings.Fields(output) {
		if strings.HasPrefix(field, "bfe1.") {
			return field
		}
	}
	t.Fatalf("no token in output:\n%s", output)
	return ""
}
