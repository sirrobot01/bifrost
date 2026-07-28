// Package selfupdate replaces the running Bifrost binary with the latest
// release archive.
//
// It deliberately works on the archive rather than the deb or rpm. The package
// name `bifrost` is already taken in the Debian and Ubuntu archives by an
// unrelated program whose version sorts above ours, so asking a package manager
// to upgrade "bifrost" can replace Bifrost with that program. Updating the
// binary in place keeps the update path under our control.
package selfupdate

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// DefaultBaseURL resolves to the assets of whichever release is current.
const DefaultBaseURL = "https://github.com/sirrobot01/bifrost/releases/latest/download"

// maxArchiveSize bounds a download that is normally a few megabytes.
const maxArchiveSize = 128 << 20

// Release identifies one downloadable archive and the checksum it must match.
type Release struct {
	Version string
	Archive string
	SHA256  string
}

type Client struct {
	BaseURL string
	HTTP    *http.Client
}

func (c *Client) baseURL() string {
	if c.BaseURL != "" {
		return c.BaseURL
	}
	return DefaultBaseURL
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

// archiveArch matches the naming that goreleaser uses for archives, which is
// the platform's own spelling rather than Go's.
func archiveArch() (string, error) {
	switch runtime.GOARCH {
	case "amd64":
		return "x86_64", nil
	case "arm64":
		return "aarch64", nil
	case "arm":
		return "armv7", nil
	default:
		return "", fmt.Errorf("no release archive is built for %s", runtime.GOARCH)
	}
}

// Latest reads the release checksum file and returns the archive built for this
// machine. The checksum file is the only index consulted, so a release that
// publishes no archive for this platform is an error rather than a silent skip.
func (c *Client) Latest(ctx context.Context) (Release, error) {
	arch, err := archiveArch()
	if err != nil {
		return Release{}, err
	}
	body, err := c.get(ctx, c.baseURL()+"/checksums.txt", 1<<20)
	if err != nil {
		return Release{}, fmt.Errorf("read release checksums: %w", err)
	}

	pattern := regexp.MustCompile(`^bifrost_(.+)_` + runtime.GOOS + `_` + regexp.QuoteMeta(arch) + `\.tar\.gz$`)
	scanner := bufio.NewScanner(bytes.NewReader(body))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 {
			continue
		}
		match := pattern.FindStringSubmatch(fields[1])
		if match == nil {
			continue
		}
		if _, err := hex.DecodeString(fields[0]); err != nil || len(fields[0]) != sha256.Size*2 {
			return Release{}, fmt.Errorf("checksum for %s is not a SHA-256 digest", fields[1])
		}
		return Release{Version: match[1], Archive: fields[1], SHA256: fields[0]}, nil
	}
	if err := scanner.Err(); err != nil {
		return Release{}, fmt.Errorf("read release checksums: %w", err)
	}
	return Release{}, fmt.Errorf("the latest release publishes no %s %s archive", runtime.GOOS, arch)
}

// Binary downloads the release archive, verifies it against the checksum from
// the release, and returns the bifrost executable inside it. Nothing is written
// to disk, so a failed verification cannot leave a partial binary behind.
func (c *Client) Binary(ctx context.Context, release Release) ([]byte, error) {
	archive, err := c.get(ctx, c.baseURL()+"/"+release.Archive, maxArchiveSize)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", release.Archive, err)
	}
	sum := sha256.Sum256(archive)
	if hex.EncodeToString(sum[:]) != release.SHA256 {
		return nil, fmt.Errorf("%s does not match its published checksum", release.Archive)
	}
	return extractBinary(archive)
}

func (c *Client) get(ctx context.Context, url string, limit int64) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	response, err := c.http().Do(request)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, limit))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) == limit {
		return nil, errors.New("response exceeded the size limit")
	}
	return body, nil
}

func extractBinary(archive []byte) ([]byte, error) {
	uncompressed, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("open archive: %w", err)
	}
	defer func() { _ = uncompressed.Close() }()

	reader := tar.NewReader(uncompressed)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read archive: %w", err)
		}
		if header.Typeflag != tar.TypeReg || filepath.Base(header.Name) != "bifrost" || strings.Contains(header.Name, "/") {
			continue
		}
		contents, err := io.ReadAll(io.LimitReader(reader, maxArchiveSize))
		if err != nil {
			return nil, fmt.Errorf("read bifrost from archive: %w", err)
		}
		return contents, nil
	}
	return nil, errors.New("the archive contains no bifrost executable")
}

// Replace swaps contents into path atomically, keeping the existing file's mode
// and ownership.
//
// The new file is written beside the target and renamed over it. Writing the
// path directly would fail with ETXTBSY while the old binary is running, and a
// rename also means an interrupted update leaves the old binary intact.
func Replace(path string, contents []byte) error {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", path, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return err
	}

	temporary, err := os.CreateTemp(filepath.Dir(resolved), ".bifrost-update-*")
	if err != nil {
		return fmt.Errorf("write beside %s: %w", resolved, err)
	}
	name := temporary.Name()
	defer func() { _ = os.Remove(name) }()

	if _, err := temporary.Write(contents); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, info.Mode().Perm()); err != nil {
		return err
	}
	if err := matchOwnership(name, info); err != nil {
		return err
	}
	return os.Rename(name, resolved)
}
