package config

import (
	"os"
	"path/filepath"
	"runtime"
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
	if config.Docker.Socket != defaultDockerSocket() {
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

func TestDecodeSelectsSpliceForEdgeServiceInAutoMode(t *testing.T) {
	t.Parallel()

	// An edge service terminates on a Bifrost listener, so auto has exactly
	// one valid answer. Selecting it beats rejecting the configuration.
	input := strings.Replace(validConfig, "dns:\n", "edge:\n  enabled: true\n  ipv4_address: 8.8.8.8\n  key_file: /etc/bifrost/edge-key\ndns:\n", 1)
	input = strings.Replace(input, "mode: splice", "mode: auto\n    edge: true", 1)
	configFile, err := Decode(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if configFile.StaticServices[0].Mode != "splice" {
		t.Fatalf("mode = %q, want splice", configFile.StaticServices[0].Mode)
	}
}

func TestDecodeRejectsDirectEdgeService(t *testing.T) {
	t.Parallel()

	// Direct mode creates no listener for the edge to reach, so an explicit
	// request for it is a genuine conflict rather than an ambiguity.
	input := strings.Replace(validConfig, "dns:\n", "edge:\n  enabled: true\n  ipv4_address: 8.8.8.8\n  key_file: /etc/bifrost/edge-key\ndns:\n", 1)
	input = strings.Replace(input, "192.0.2.10:8096", `"[::]:443"`, 1)
	input = strings.Replace(input, "mode: splice", "mode: direct\n    edge: true", 1)
	if _, err := Decode(strings.NewReader(input)); err == nil || !strings.Contains(err.Error(), "must use splice") {
		t.Fatalf("error = %v", err)
	}
}

func TestReadSecretRejectsBroadPermissions(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("Windows access-control lists are covered by the platform test")
	}

	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSecret(path); err == nil || !strings.Contains(err.Error(), "permissions") {
		t.Fatalf("error = %v", err)
	}
}

func TestExampleConfigDecodes(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("the packaged example uses Unix paths")
	}

	file, err := os.Open("../../configs/bifrost.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	if _, err := Decode(file); err != nil {
		t.Fatal(err)
	}
}

// Configuration a tool can work out for itself is ceremony. A file naming only
// an interface and a service must load.
func TestDecodeFillsInWhatItCan(t *testing.T) {
	t.Parallel()

	minimal := `version: 1
interface: eth0
dns:
  provider: desec
  ttl: 3600s
  desec:
    zone: example.com
    token_file: /etc/bifrost/desec-token
static_services:
  - name: media
    backend: 127.0.0.1:8096
    listen: 443
    dns: media.example.com
`
	configFile, err := Decode(strings.NewReader(minimal))
	if err != nil {
		t.Fatalf("a minimal configuration was rejected: %v", err)
	}
	if configFile.OwnerID == "" {
		t.Fatal("owner_id was not derived")
	}
	if configFile.SecretFile != filepath.Join(defaultConfigDirectory(), "address-secret") {
		t.Fatalf("secret_file = %q", configFile.SecretFile)
	}
	// Defaults must not silently override what the operator did state.
	stated := strings.Replace(minimal, "interface: eth0", "interface: eth0\nowner_id: chosen-name", 1)
	configFile, err = Decode(strings.NewReader(stated))
	if err != nil {
		t.Fatal(err)
	}
	if configFile.OwnerID != "chosen-name" {
		t.Fatalf("owner_id = %q, want the stated value", configFile.OwnerID)
	}
}
