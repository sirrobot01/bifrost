package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
)

// checksumServer publishes one archive entry for this platform. --check never
// downloads it, so the body only has to exist.
func checksumServer(t *testing.T, version string) *httptest.Server {
	t.Helper()
	arch := map[string]string{"amd64": "x86_64", "arm64": "aarch64", "arm": "armv7"}[runtime.GOARCH]
	if arch == "" {
		t.Skipf("no release archive for %s", runtime.GOARCH)
	}
	sum := sha256.Sum256(nil)
	body := fmt.Sprintf("%s  bifrost_%s_%s_%s.tar.gz\n", hex.EncodeToString(sum[:]), version, runtime.GOOS, arch)

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/checksums.txt" {
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = writer.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server
}

func TestRunnerUpgradeCheckReportsANewerRelease(t *testing.T) {
	t.Parallel()

	server := checksumServer(t, "0.9.9")
	var stdout, stderr bytes.Buffer
	runner := Runner{Stdout: &stdout, Stderr: &stderr, Version: "0.5.3"}
	if code := runner.Run(t.Context(), []string{"upgrade", "--check", "--base-url", server.URL}); code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, stderr.String())
	}
	for _, expected := range []string{"installed 0.5.3", "latest 0.9.9", "sudo bifrost upgrade"} {
		if !strings.Contains(stdout.String(), expected) {
			t.Fatalf("stdout does not mention %q:\n%s", expected, stdout.String())
		}
	}
}

// An up-to-date host must not be told to run anything, and --check must never
// touch the binary regardless.
func TestRunnerUpgradeCheckIsQuietWhenCurrent(t *testing.T) {
	t.Parallel()

	server := checksumServer(t, "0.5.3")
	var stdout, stderr bytes.Buffer
	runner := Runner{Stdout: &stdout, Stderr: &stderr, Version: "v0.5.3"}
	if code := runner.Run(t.Context(), []string{"upgrade", "--check", "--base-url", server.URL}); code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, stderr.String())
	}
	if strings.Contains(stdout.String(), "sudo bifrost upgrade") {
		t.Fatalf("a current host was told to upgrade:\n%s", stdout.String())
	}
}

// Without --force, a matching version must stop before downloading anything.
func TestRunnerUpgradeStopsWhenAlreadyCurrent(t *testing.T) {
	t.Parallel()

	server := checksumServer(t, "0.5.3")
	var stdout, stderr bytes.Buffer
	runner := Runner{Stdout: &stdout, Stderr: &stderr, Version: "0.5.3"}
	if code := runner.Run(t.Context(), []string{"upgrade", "--base-url", server.URL}); code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Already on the latest release") {
		t.Fatalf("stdout = %s", stdout.String())
	}
}
