package certauto

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func selfSigned(t *testing.T, name string, notAfter time.Time) ([]byte, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: name},
		DNSNames:     []string{name},
		NotBefore:    notAfter.Add(-90 * 24 * time.Hour),
		NotAfter:     notAfter,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certificatePEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return certificatePEM, keyPEM
}

func TestManagerIssuesCachesAndRenews(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 27, 12, 0, 0, 0, time.UTC)
	expiry := now.Add(90 * 24 * time.Hour)
	issued := 0
	manager, err := NewManager(Config{
		StateDir: t.TempDir(),
		Now:      func() time.Time { return now },
		issue: func(_ context.Context, name string) ([]byte, []byte, error) {
			issued++
			certificatePEM, keyPEM := selfSigned(t, name, expiry)
			return certificatePEM, keyPEM, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	first, err := manager.Certificate(t.Context(), "media.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Certificate(t.Context(), "media.example.com"); err != nil {
		t.Fatal(err)
	}
	if issued != 1 {
		t.Fatalf("issued = %d, want 1 (second call must hit the cache)", issued)
	}

	// Inside the renewal window, RenewDue reissues and the TLS callback serves
	// the replacement without any listener involvement.
	now = expiry.Add(-15 * 24 * time.Hour)
	expiry = now.Add(90 * 24 * time.Hour)
	renewed, err := manager.RenewDue(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(renewed) != 1 || renewed[0] != "media.example.com" || issued != 2 {
		t.Fatalf("renewed = %v, issued = %d", renewed, issued)
	}
	served, err := manager.TLSConfig("media.example.com").GetCertificate(&tls.ClientHelloInfo{})
	if err != nil {
		t.Fatal(err)
	}
	if served.Leaf.NotAfter.Equal(first.Leaf.NotAfter) {
		t.Fatal("TLS callback still serves the pre-renewal certificate")
	}
}

func TestManagerSharesOneIssuancePerName(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 27, 12, 0, 0, 0, time.UTC)
	var issued atomic.Int32
	manager, err := NewManager(Config{
		StateDir: t.TempDir(),
		Now:      func() time.Time { return now },
		issue: func(_ context.Context, name string) ([]byte, []byte, error) {
			issued.Add(1)
			time.Sleep(50 * time.Millisecond) // widen the race window
			certificatePEM, keyPEM := selfSigned(t, name, now.Add(90*24*time.Hour))
			return certificatePEM, keyPEM, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	var wait sync.WaitGroup
	for range 5 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if _, err := manager.Certificate(context.Background(), "media.example.com"); err != nil {
				t.Error(err)
			}
		}()
	}
	wait.Wait()
	if issued.Load() != 1 {
		t.Fatalf("issued = %d, want 1 (concurrent callers must share one issuance)", issued.Load())
	}
}

func TestManagerLoadsFromDiskAndProtectsKeys(t *testing.T) {
	t.Parallel()

	stateDir := t.TempDir()
	now := time.Date(2026, time.July, 27, 12, 0, 0, 0, time.UTC)
	issued := 0
	newManager := func() *Manager {
		manager, err := NewManager(Config{
			StateDir: stateDir,
			Now:      func() time.Time { return now },
			issue: func(_ context.Context, name string) ([]byte, []byte, error) {
				issued++
				certificatePEM, keyPEM := selfSigned(t, name, now.Add(90*24*time.Hour))
				return certificatePEM, keyPEM, nil
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		return manager
	}

	if _, err := newManager().Certificate(t.Context(), "media.example.com"); err != nil {
		t.Fatal(err)
	}
	// A fresh manager over the same state directory must reuse the stored
	// certificate instead of issuing again.
	if _, err := newManager().Certificate(t.Context(), "media.example.com"); err != nil {
		t.Fatal(err)
	}
	if issued != 1 {
		t.Fatalf("issued = %d, want 1 (restart must reuse the stored certificate)", issued)
	}
	info, err := os.Stat(stateDir + "/media.example.com.key")
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("key mode = %04o, want 0600", info.Mode().Perm())
	}
}

// The point of wildcard issuance: several services under one parent cost one
// ACME order, not one each.
func TestManagerIssuesOneWildcardForSiblingNames(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 27, 12, 0, 0, 0, time.UTC)
	var issuedFor []string
	manager, err := NewManager(Config{
		StateDir: t.TempDir(),
		Wildcard: true,
		Now:      func() time.Time { return now },
		issue: func(_ context.Context, name string) ([]byte, []byte, error) {
			issuedFor = append(issuedFor, name)
			certificatePEM, keyPEM := selfSigned(t, name, now.Add(90*24*time.Hour))
			return certificatePEM, keyPEM, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"media.example.com", "photos.example.com", "books.example.com"} {
		if _, err := manager.Certificate(t.Context(), name); err != nil {
			t.Fatalf("Certificate(%s): %v", name, err)
		}
	}
	if len(issuedFor) != 1 || issuedFor[0] != "*.example.com" {
		t.Fatalf("issued %v, want one order for *.example.com", issuedFor)
	}

	// A deeper name is not covered by the parent wildcard, so it gets its own.
	if _, err := manager.Certificate(t.Context(), "cam.house.example.com"); err != nil {
		t.Fatal(err)
	}
	if len(issuedFor) != 2 || issuedFor[1] != "*.house.example.com" {
		t.Fatalf("issued %v, want a second order for *.house.example.com", issuedFor)
	}
}

// A wildcard never reaches a file name, where an asterisk reads as a glob.
func TestManagerStoresWildcardsUnderASafeFileName(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 27, 12, 0, 0, 0, time.UTC)
	directory := t.TempDir()
	manager, err := NewManager(Config{
		StateDir: directory,
		Wildcard: true,
		Now:      func() time.Time { return now },
		issue: func(_ context.Context, name string) ([]byte, []byte, error) {
			certificatePEM, keyPEM := selfSigned(t, name, now.Add(90*24*time.Hour))
			return certificatePEM, keyPEM, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Certificate(t.Context(), "media.example.com"); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
		if strings.Contains(entry.Name(), "*") {
			t.Fatalf("state directory holds %q", entry.Name())
		}
	}
	if !slices.Contains(names, "_wildcard.example.com.crt") {
		t.Fatalf("state directory = %v", names)
	}
}

// Without the setting, nothing changes: one certificate per published name.
func TestManagerKeepsPerNameCertificatesByDefault(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 27, 12, 0, 0, 0, time.UTC)
	var issuedFor []string
	manager, err := NewManager(Config{
		StateDir: t.TempDir(),
		Now:      func() time.Time { return now },
		issue: func(_ context.Context, name string) ([]byte, []byte, error) {
			issuedFor = append(issuedFor, name)
			certificatePEM, keyPEM := selfSigned(t, name, now.Add(90*24*time.Hour))
			return certificatePEM, keyPEM, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"media.example.com", "photos.example.com"} {
		if _, err := manager.Certificate(t.Context(), name); err != nil {
			t.Fatal(err)
		}
	}
	if len(issuedFor) != 2 {
		t.Fatalf("issued %v, want one per name", issuedFor)
	}
}

// A two-label name has no parent worth wildcarding, since *.com is not
// issuable.
func TestWildcardSubjectLeavesShortNamesAlone(t *testing.T) {
	t.Parallel()

	if _, ok := wildcardSubject("example.com"); ok {
		t.Fatal("example.com was wildcarded")
	}
	if subject, ok := wildcardSubject("a.b.example.com"); !ok || subject != "*.b.example.com" {
		t.Fatalf("wildcardSubject(a.b.example.com) = %q, %v", subject, ok)
	}
	if subject, ok := wildcardSubject("*.example.com"); !ok || subject != "*.example.com" {
		t.Fatalf("an existing wildcard was rewritten to %q", subject)
	}
}
