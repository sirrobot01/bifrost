package cli

import (
	"bytes"
	"context"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sirrobot01/bifrost/internal/netwatch"
	"github.com/sirrobot01/bifrost/internal/serviceaddr"
)

func TestRunnerInitWritesStrictConfig(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	runner := Runner{Stdout: &stdout, Stderr: &stderr, Version: "test"}
	code := runner.Run(context.Background(), []string{"init", "--interface", "lo"})
	if code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, stderr.String())
	}
	output := stdout.String()
	for _, expected := range []string{"version: 1", "interface: lo", "provider: cloudflare", "mode: advisory"} {
		if !strings.Contains(output, expected) {
			t.Fatalf("output does not contain %q:\n%s", expected, output)
		}
	}
}

func TestObservedAddressUsesStableCandidate(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 25, 12, 0, 0, 0, time.UTC)
	snapshot := netwatch.Snapshot{Candidates: []serviceaddr.Candidate{
		{Prefix: netip.MustParsePrefix("2001:db8:1::20/64"), Temporary: true},
		{Prefix: netip.MustParsePrefix("2001:db8:1::30/64")},
		{Prefix: netip.MustParsePrefix("2001:db8:1::10/64")},
	}}
	address, err := observedAddress(snapshot, serviceaddr.Selection{Prefix: netip.MustParsePrefix("2001:db8:1::/64")}, now)
	if err != nil {
		t.Fatal(err)
	}
	if want := netip.MustParseAddr("2001:db8:1::10"); address != want {
		t.Fatalf("address = %s, want %s", address, want)
	}
}

func TestRunnerStatusExplainsDirectMode(t *testing.T) {
	t.Parallel()

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	input := `
version: 1
interface: lo
owner_id: home
secret_file: /unused
dns:
  provider: cloudflare
  cloudflare:
    zone_id: zone
    api_token_file: /unused
static_services:
  - name: media
    backend: "[2001:4860::10]:443"
    listen: 443
    dns: media.example.com
    mode: direct
    public_address: "2001:4860::10"
`
	if err := os.WriteFile(configPath, []byte(input), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	runner := Runner{Stdout: &stdout, Stderr: &stderr, Version: "test"}
	code := runner.Run(context.Background(), []string{"status", "--config", configPath})
	if code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, stderr.String())
	}
	if output := stdout.String(); !strings.Contains(output, "media: direct") || !strings.Contains(output, "client IP preserved: true") {
		t.Fatalf("status = %s", output)
	}
}

func TestRunnerRejectsUnknownCommand(t *testing.T) {
	t.Parallel()

	var stderr bytes.Buffer
	runner := Runner{Stdout: &bytes.Buffer{}, Stderr: &stderr, Version: "test"}
	if code := runner.Run(context.Background(), []string{"unknown"}); code != 1 {
		t.Fatalf("code = %d", code)
	}
	if !strings.Contains(stderr.String(), "unknown command") {
		t.Fatalf("stderr = %s", stderr.String())
	}
}
