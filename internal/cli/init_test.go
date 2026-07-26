package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sirrobot01/bifrost/internal/config"
)

// answerFile turns scripted answers into a file usable as Runner.Stdin.
func answerFile(t *testing.T, answers ...string) *os.File {
	t.Helper()
	path := filepath.Join(t.TempDir(), "answers")
	if err := os.WriteFile(path, []byte(strings.Join(answers, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	return file
}

func TestGuessZoneKnowsSharedSuffixes(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"media.example.com":        "example.com",
		"example.com":              "example.com",
		"plex.sirrobot01.dedyn.io": "sirrobot01.dedyn.io",
		"a.home.example.dynv6.net": "example.dynv6.net",
	}
	for name, want := range tests {
		if got := guessZone(name); got != want {
			t.Errorf("guessZone(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestRunnerInitInteractiveCreatesEveryFile(t *testing.T) {
	t.Parallel()

	configDir := filepath.Join(t.TempDir(), "bifrost")
	stdin := answerFile(t,
		configDir,           // configuration directory
		"media",             // service name
		"media.example.com", // public DNS name
		"127.0.0.1:8096",    // backend
		"443",               // public port
		"auto",              // mode
		"desec",             // DNS provider
		"desec-token-value", // token
		"",                  // zone: accept the discovered default
		"y",                 // write the files
	)

	var stdout, stderr bytes.Buffer
	runner := Runner{Stdout: &stdout, Stderr: &stderr, Stdin: stdin, Version: "test",
		zoneLookup: func(_ context.Context, provider, token, dnsName string) (string, error) {
			if provider != "desec" || token != "desec-token-value" || dnsName != "media.example.com" {
				t.Errorf("lookup got provider=%q token=%q name=%q", provider, token, dnsName)
			}
			return "example.com", nil
		}}
	if code := runner.Run(t.Context(), []string{"init", "--interactive", "--interface", "lo", "--config-dir", configDir}); code != 0 {
		t.Fatalf("code = %d, stderr = %s, stdout = %s", code, stderr.String(), stdout.String())
	}

	configPath := filepath.Join(configDir, "config.yaml")
	loaded, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("generated config does not load: %v", err)
	}
	if len(loaded.StaticServices) != 1 || loaded.StaticServices[0].DNSName != "media.example.com" {
		t.Fatalf("services = %+v", loaded.StaticServices)
	}
	if loaded.DNS.Provider != "desec" || loaded.DNS.DESEC.Zone != "example.com" {
		t.Fatalf("dns = %+v", loaded.DNS)
	}

	// Every generated file must be unreadable by group and other, because
	// config.ReadSecret refuses secrets that are not.
	for _, name := range []string{"config.yaml", "address-secret", "desec-token"} {
		info, err := os.Stat(filepath.Join(configDir, name))
		if err != nil {
			t.Fatalf("%s was not created: %v", name, err)
		}
		if permissions := info.Mode().Perm(); permissions != 0o600 {
			t.Fatalf("%s mode = %04o, want 0600", name, permissions)
		}
	}

	secret, err := config.ReadSecret(filepath.Join(configDir, "address-secret"))
	if err != nil {
		t.Fatalf("generated address secret is unusable: %v", err)
	}
	if len(secret) < 32 {
		t.Fatalf("address secret is %d bytes, want at least 32", len(secret))
	}
	if token, err := config.ReadSecret(filepath.Join(configDir, "desec-token")); err != nil || string(token) != "desec-token-value" {
		t.Fatalf("token = %q, err = %v", token, err)
	}
}

func TestRunnerInitInteractiveWritesNothingWhenDeclined(t *testing.T) {
	t.Parallel()

	configDir := filepath.Join(t.TempDir(), "bifrost")
	stdin := answerFile(t,
		configDir,
		"media",
		"media.example.com",
		"127.0.0.1:8096",
		"443",
		"auto",
		"desec",
		"example.com",
		"desec-token-value",
		"n", // decline
	)

	var stdout, stderr bytes.Buffer
	runner := Runner{Stdout: &stdout, Stderr: &stderr, Stdin: stdin, Version: "test"}
	if code := runner.Run(t.Context(), []string{"init", "--interactive", "--interface", "lo", "--config-dir", configDir}); code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if _, err := os.Stat(configDir); !os.IsNotExist(err) {
		t.Fatalf("declining still created %s", configDir)
	}
}

func TestRunnerInitInteractiveRejectsDirectModeForIPv4Backend(t *testing.T) {
	t.Parallel()

	configDir := filepath.Join(t.TempDir(), "bifrost")
	stdin := answerFile(t,
		configDir,
		"media",
		"media.example.com",
		"127.0.0.1:8096",
		"443",
		"direct", // not offered for an IPv4 backend
		"direct",
		"direct",
	)

	var stdout, stderr bytes.Buffer
	runner := Runner{Stdout: &stdout, Stderr: &stderr, Stdin: stdin, Version: "test"}
	if code := runner.Run(t.Context(), []string{"init", "--interactive", "--interface", "lo", "--config-dir", configDir}); code == 0 {
		t.Fatal("init accepted direct mode for an IPv4 backend")
	}
	if !strings.Contains(stdout.String(), "direct mode is unavailable") {
		t.Fatalf("stdout = %s", stdout.String())
	}
}

func TestRunnerInitTemplateNamesWhatIsStillMissing(t *testing.T) {
	t.Parallel()

	outputPath := filepath.Join(t.TempDir(), "config.yaml")
	var stdout, stderr bytes.Buffer
	runner := Runner{Stdout: &stdout, Stderr: &stderr, Version: "test"}
	if code := runner.Run(t.Context(), []string{"init", "--interface", "lo", "--output", outputPath}); code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, stderr.String())
	}
	// The template is not runnable, so the command has to say so rather than
	// leave the operator to discover it at serve time.
	for _, expected := range []string{"not yet usable", "static_services", "--interactive"} {
		if !strings.Contains(stdout.String(), expected) {
			t.Fatalf("stdout does not mention %q:\n%s", expected, stdout.String())
		}
	}
}
