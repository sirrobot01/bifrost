package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sirrobot01/bifrost/internal/config"
)

const publishBase = `version: 1
interface: eth0

dns:
  provider: desec
  desec:
    zone: example.com
    token_file: /etc/bifrost/desec-token

# Services published from this host.
static_services:
  - name: media
    backend: 127.0.0.1:8096
    listen: 443
    dns: media.example.com

metrics:
  listen: 127.0.0.1:9098
`

func publishConfig(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func runPublish(t *testing.T, arguments ...string) (string, string, int) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	runner := Runner{Stdout: &stdout, Stderr: &stderr, Version: "test"}
	code := runner.Run(t.Context(), append([]string{"publish"}, arguments...))
	return stdout.String(), stderr.String(), code
}

// The file an operator wrote has to survive the edit: a round trip through the
// config struct would delete every comment in it.
func TestPublishAddsAServiceAndKeepsTheRestOfTheFile(t *testing.T) {
	t.Parallel()

	path := publishConfig(t, publishBase)
	_, stderr, code := runPublish(t, "--config", path, "--no-reload", "photos.example.com", "127.0.0.1:2283")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, stderr)
	}

	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(written), "# Services published from this host.") {
		t.Fatalf("the comment was lost:\n%s", written)
	}
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("the edited file does not load: %v", err)
	}
	if len(loaded.StaticServices) != 2 {
		t.Fatalf("services = %+v", loaded.StaticServices)
	}
	added := loaded.StaticServices[1]
	if added.Name != "photos" || added.DNSName != "photos.example.com" || added.Backend != "127.0.0.1:2283" || added.Listen != 443 {
		t.Fatalf("added = %+v", added)
	}
	// The service has to land inside static_services, not after the next key.
	if strings.Index(string(written), "photos.example.com") > strings.Index(string(written), "metrics:") {
		t.Fatalf("the service was appended outside the block:\n%s", written)
	}
}

func TestPublishFillsAnEmptyServiceList(t *testing.T) {
	t.Parallel()

	path := publishConfig(t, strings.Replace(publishBase, `static_services:
  - name: media
    backend: 127.0.0.1:8096
    listen: 443
    dns: media.example.com`, "static_services: []", 1))

	if _, stderr, code := runPublish(t, "--config", path, "--no-reload", "media.example.com", "127.0.0.1:8096"); code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, stderr)
	}
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("the edited file does not load: %v", err)
	}
	if len(loaded.StaticServices) != 1 || loaded.StaticServices[0].Name != "media" {
		t.Fatalf("services = %+v", loaded.StaticServices)
	}
}

func TestPublishHonoursTLSAndEdgeFlags(t *testing.T) {
	t.Parallel()

	path := publishConfig(t, publishBase)
	if _, stderr, code := runPublish(t, "--config", path, "--no-reload", "--tls", "off", "--listen", "32400", "--name", "plexserver", "plex.example.com", "127.0.0.1:32400"); code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, stderr)
	}
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	added := loaded.StaticServices[1]
	if added.Name != "plexserver" || added.TLS != "off" || added.Listen != 32400 {
		t.Fatalf("added = %+v", added)
	}
}

// A duplicate would produce two services fighting over one name, so it is
// refused before anything is written.
func TestPublishRefusesDuplicates(t *testing.T) {
	t.Parallel()

	// Go's flag package stops at the first positional, so flags come first.
	for _, arguments := range [][]string{
		{"media.example.com", "127.0.0.1:9000"},
		{"--name", "media", "other.example.com", "127.0.0.1:9000"},
	} {
		path := publishConfig(t, publishBase)
		before, _ := os.ReadFile(path)
		_, stderr, code := runPublish(t, append([]string{"--config", path, "--no-reload"}, arguments...)...)
		if code == 0 {
			t.Fatalf("%v was accepted", arguments)
		}
		if !strings.Contains(stderr, "already") {
			t.Fatalf("stderr = %s", stderr)
		}
		after, _ := os.ReadFile(path)
		if string(before) != string(after) {
			t.Fatal("a rejected publish still modified the file")
		}
	}
}

func TestPublishDryRunWritesNothing(t *testing.T) {
	t.Parallel()

	path := publishConfig(t, publishBase)
	before, _ := os.ReadFile(path)
	stdout, stderr, code := runPublish(t, "--config", path, "--dry-run", "photos.example.com", "127.0.0.1:2283")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, stderr)
	}
	if !strings.Contains(stdout, "dns: photos.example.com") {
		t.Fatalf("stdout = %s", stdout)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("--dry-run modified the file")
	}
}

func TestPublishRejectsABadBackend(t *testing.T) {
	t.Parallel()

	path := publishConfig(t, publishBase)
	if _, stderr, code := runPublish(t, "--config", path, "--no-reload", "photos.example.com", "localhost:2283"); code == 0 {
		t.Fatal("a host name was accepted as a backend")
	} else if !strings.Contains(stderr, "IP address and port") {
		t.Fatalf("stderr = %s", stderr)
	}
}

// A file whose services are not a plain top-level list is reported rather than
// mangled.
func TestPublishRefusesAFileItCannotEditSafely(t *testing.T) {
	t.Parallel()

	path := publishConfig(t, strings.Replace(publishBase, `static_services:
  - name: media
    backend: 127.0.0.1:8096
    listen: 443
    dns: media.example.com`, `static_services: [{name: media, backend: "127.0.0.1:8096", listen: 443, dns: media.example.com}]`, 1))

	if _, stderr, code := runPublish(t, "--config", path, "--no-reload", "photos.example.com", "127.0.0.1:2283"); code == 0 {
		t.Fatal("an inline list was edited")
	} else if !strings.Contains(stderr, "publish can edit") {
		t.Fatalf("stderr = %s", stderr)
	}
}
