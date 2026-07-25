package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const validConfig = `
version: 1
interface: eth0
owner_id: home
secret_file: /etc/bifrost/secret
dns:
  provider: cloudflare
  ttl: 60s
  cloudflare:
    zone_id: zone
    api_token_file: /etc/bifrost/cloudflare-token
firewall:
  mode: advisory
metrics:
  listen: 127.0.0.1:9098
docker:
  enabled: false
static_services:
  - name: media
    backend: 192.0.2.10:8096
    listen: 443
    dns: media.example.com
    mode: splice
`

func TestDecodeAppliesDefaults(t *testing.T) {
	t.Parallel()

	config, err := Decode(strings.NewReader(validConfig))
	if err != nil {
		t.Fatal(err)
	}
	if config.SettleWindow.Duration() != 10*time.Second || config.DrainGrace.Duration() != 2*time.Minute {
		t.Fatalf("durations = %s, %s", config.SettleWindow.Duration(), config.DrainGrace.Duration())
	}
	if config.Docker.Socket != "/var/run/docker.sock" {
		t.Fatalf("Docker socket = %q", config.Docker.Socket)
	}
}

func TestDecodeRejectsUnknownField(t *testing.T) {
	t.Parallel()

	_, err := Decode(strings.NewReader(validConfig + "unknown: true\n"))
	if err == nil || !strings.Contains(err.Error(), "field unknown not found") {
		t.Fatalf("error = %v", err)
	}
}

func TestDecodeRejectsUnsafeDirectMode(t *testing.T) {
	t.Parallel()

	input := strings.Replace(validConfig, "mode: splice", "mode: direct", 1)
	if _, err := Decode(strings.NewReader(input)); err == nil || !strings.Contains(err.Error(), "direct mode") {
		t.Fatalf("error = %v", err)
	}
}

func TestDecodeAllowsObservedDirectAddress(t *testing.T) {
	t.Parallel()

	input := strings.Replace(validConfig, "192.0.2.10:8096", `"[::]:443"`, 1)
	input = strings.Replace(input, "mode: splice", "mode: direct", 1)
	if _, err := Decode(strings.NewReader(input)); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeRequiresSpliceForEdgeService(t *testing.T) {
	t.Parallel()

	input := strings.Replace(validConfig, "dns:\n", "edge:\n  enabled: true\n  ipv4_address: 8.8.8.8\n  key_file: /etc/bifrost/edge-key\ndns:\n", 1)
	input = strings.Replace(input, "mode: splice", "mode: auto\n    edge: true", 1)
	if _, err := Decode(strings.NewReader(input)); err == nil || !strings.Contains(err.Error(), "must use splice") {
		t.Fatalf("error = %v", err)
	}
}

func TestReadSecretRejectsBroadPermissions(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSecret(path); err == nil || !strings.Contains(err.Error(), "permissions") {
		t.Fatalf("error = %v", err)
	}
}
