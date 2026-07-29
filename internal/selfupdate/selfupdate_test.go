package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// releaseArchive builds the tar.gz that goreleaser publishes: the binary at the
// root beside the documentation files.
func releaseArchive(t *testing.T, binary string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	compressor := gzip.NewWriter(&buffer)
	writer := tar.NewWriter(compressor)
	for _, file := range []struct{ name, body string }{
		{"LICENSE", "MIT"},
		{executableName(), binary},
	} {
		if err := writer.WriteHeader(&tar.Header{Name: file.name, Typeflag: tar.TypeReg, Mode: 0o755, Size: int64(len(file.body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write([]byte(file.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := compressor.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func archiveName(t *testing.T, version string) string {
	t.Helper()
	arch, err := archiveArch()
	if err != nil {
		t.Skipf("no release archive for %s", runtime.GOARCH)
	}
	return fmt.Sprintf("bifrost_%s_%s_%s.tar.gz", version, runtime.GOOS, arch)
}

// releaseServer serves a checksum file and archive the way the release assets
// do, optionally corrupting the archive to exercise verification.
func releaseServer(t *testing.T, version, binary string, corrupt bool) *Client {
	t.Helper()
	name := archiveName(t, version)
	archive := releaseArchive(t, binary)
	sum := sha256.Sum256(archive)
	if corrupt {
		archive = append(archive, 'x')
	}
	// A real checksum file lists every platform, so the client has to select
	// rather than take the first line.
	checksums := fmt.Sprintf("%s  bifrost_%s_linux_unrelated.tar.gz\n%s  %s\n",
		strings.Repeat("0", 64), version, hex.EncodeToString(sum[:]), name)

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/checksums.txt":
			_, _ = writer.Write([]byte(checksums))
		case "/" + name:
			_, _ = writer.Write(archive)
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return &Client{BaseURL: server.URL, HTTP: server.Client()}
}

func TestLatestSelectsThisPlatformsArchive(t *testing.T) {
	t.Parallel()

	client := releaseServer(t, "0.5.3", "new binary", false)
	release, err := client.Latest(t.Context())
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if release.Version != "0.5.3" {
		t.Fatalf("version = %q", release.Version)
	}
	if release.Archive != archiveName(t, "0.5.3") {
		t.Fatalf("archive = %q", release.Archive)
	}

	binary, err := client.Binary(t.Context(), release)
	if err != nil {
		t.Fatalf("Binary: %v", err)
	}
	if string(binary) != "new binary" {
		t.Fatalf("binary = %q", binary)
	}
}

// A tampered archive must never reach the disk, so verification failure has to
// happen before Replace is ever called.
func TestBinaryRejectsAChecksumMismatch(t *testing.T) {
	t.Parallel()

	client := releaseServer(t, "0.5.3", "new binary", true)
	release, err := client.Latest(t.Context())
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if _, err := client.Binary(t.Context(), release); err == nil {
		t.Fatal("a corrupted archive was accepted")
	} else if !strings.Contains(err.Error(), "published checksum") {
		t.Fatalf("error = %v", err)
	}
}

func TestLatestReportsAMissingPlatform(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/checksums.txt" {
			_, _ = fmt.Fprintf(writer, "%s  bifrost_0.5.3_linux_unrelated.tar.gz\n", strings.Repeat("0", 64))
			return
		}
		writer.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(server.Close)

	client := &Client{BaseURL: server.URL, HTTP: server.Client()}
	if _, err := client.Latest(t.Context()); err == nil {
		t.Fatal("a release without this platform was accepted")
	}
}

// Replace has to survive the binary being busy, which is the normal case: the
// process doing the upgrade is running the file it overwrites.
func TestReplaceKeepsModeAndReplacesAtomically(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("Windows upgrades are performed by install.ps1")
	}

	directory := t.TempDir()
	path := filepath.Join(directory, "bifrost")
	if err := os.WriteFile(path, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Replace(path, []byte("new binary")); err != nil {
		t.Fatalf("Replace: %v", err)
	}

	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "new binary" {
		t.Fatalf("contents = %q", contents)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("mode = %04o, want 0755", info.Mode().Perm())
	}

	// The temporary file is written into the target's directory so the rename
	// stays on one filesystem. None of them may survive.
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("directory holds %d entries, want only the binary", len(entries))
	}
}

// A symlinked path, which is how /usr/local/bin often reaches a real binary,
// must update the target rather than replace the link with a regular file.
func TestReplaceFollowsASymlink(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("Windows upgrades are performed by install.ps1")
	}

	directory := t.TempDir()
	target := filepath.Join(directory, "bifrost-real")
	link := filepath.Join(directory, "bifrost")
	if err := os.WriteFile(target, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := Replace(link, []byte("new binary")); err != nil {
		t.Fatalf("Replace: %v", err)
	}

	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("the symlink was replaced by a regular file")
	}
	contents, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "new binary" {
		t.Fatalf("target contents = %q", contents)
	}
}
