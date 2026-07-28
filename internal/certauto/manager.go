// Package certauto obtains and renews TLS certificates for published service
// names through ACME DNS-01 challenges. The challenge records are managed
// through the same DNS provider credentials Bifrost already holds for
// publication, so every supported provider gets certificates without any
// provider-specific code.
package certauto

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sirrobot01/bifrost/internal/dnspublish"
)

// renewBefore is how long before expiry a certificate is reissued. Let's
// Encrypt certificates live 90 days; 30 days of margin absorbs weeks of
// renewal failures before anything user-visible breaks.
const renewBefore = 30 * 24 * time.Hour

type Config struct {
	Provider dnspublish.Provider
	// StateDir holds the ACME account key and issued certificates. It is
	// machine state, not operator configuration.
	StateDir string
	// Email receives expiry warnings from the CA. Optional.
	Email string
	// DirectoryURL selects the ACME service. Empty means Let's Encrypt.
	DirectoryURL string
	// ChallengeTTL is the TTL for challenge TXT records. It follows dns.ttl so
	// provider minimums (deSEC enforces 3600s) hold for challenges too.
	ChallengeTTL time.Duration
	Logger       *slog.Logger
	Now          func() time.Time
	// issue overrides the ACME flow in tests.
	issue issueFunc
}

type issueFunc func(ctx context.Context, name string) (certificatePEM, keyPEM []byte, err error)

// Manager guards its certificate cache with a short-lived read-write lock and
// serializes issuance per name. Issuance for different names runs
// concurrently, and the serving callback never waits behind an issuance:
// during a renewal the previous certificate keeps being served.
type Manager struct {
	config    Config
	mu        sync.RWMutex
	certs     map[string]*tls.Certificate
	inflight  map[string]*sync.Mutex
	accountMu sync.Mutex
}

func NewManager(config Config) (*Manager, error) {
	if config.Provider == nil && config.issue == nil {
		return nil, errors.New("certificate manager needs a DNS provider")
	}
	if !filepath.IsAbs(config.StateDir) {
		return nil, errors.New("certificate state directory must be an absolute path")
	}
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.ChallengeTTL <= 0 {
		config.ChallengeTTL = time.Hour
	}
	if err := os.MkdirAll(config.StateDir, 0o700); err != nil {
		return nil, fmt.Errorf("create certificate state directory: %w", err)
	}
	manager := &Manager{config: config, certs: make(map[string]*tls.Certificate), inflight: make(map[string]*sync.Mutex)}
	if manager.config.issue == nil {
		manager.config.issue = manager.acmeIssue
	}
	return manager, nil
}

// Certificate returns a certificate for name, issuing one when neither the
// cache nor the state directory holds a usable one. It blocks for the
// issuance, so it belongs in the reconcile path, not the accept path.
// Concurrent calls for the same name share one issuance; different names
// issue independently.
func (m *Manager) Certificate(ctx context.Context, name string) (*tls.Certificate, error) {
	name = strings.ToLower(strings.TrimSuffix(name, "."))
	if certificate, ok := m.cached(name); ok && !m.needsRenewal(certificate) {
		return certificate, nil
	}
	lock := m.nameLock(name)
	lock.Lock()
	defer lock.Unlock()
	// A concurrent caller may have finished the work while this one waited.
	if certificate, ok := m.cached(name); ok && !m.needsRenewal(certificate) {
		return certificate, nil
	}
	if certificate, err := m.load(name); err == nil && !m.needsRenewal(certificate) {
		m.remember(name, certificate)
		return certificate, nil
	}
	return m.issueAndStore(ctx, name)
}

// RenewDue reissues every known certificate inside the renewal window and
// reports the names renewed. The cache lock is never held across an issuance,
// so handshakes keep serving the old certificate while its replacement is
// obtained.
func (m *Manager) RenewDue(ctx context.Context) ([]string, error) {
	m.mu.RLock()
	names := make([]string, 0, len(m.certs))
	for name := range m.certs {
		names = append(names, name)
	}
	m.mu.RUnlock()

	var renewed []string
	var renewErrors []error
	for _, name := range names {
		certificate, ok := m.cached(name)
		if !ok || !m.needsRenewal(certificate) {
			continue
		}
		lock := m.nameLock(name)
		lock.Lock()
		if certificate, ok := m.cached(name); ok && m.needsRenewal(certificate) {
			if _, err := m.issueAndStore(ctx, name); err != nil {
				renewErrors = append(renewErrors, fmt.Errorf("renew %s: %w", name, err))
			} else {
				renewed = append(renewed, name)
			}
		}
		lock.Unlock()
	}
	return renewed, errors.Join(renewErrors...)
}

// Expiries reports when each held certificate stops being valid, so a
// renewal that has been failing becomes visible before the service breaks.
// Renewal runs 30 days out, so anything materially lower has been failing for
// a while and nothing else would have said so.
func (m *Manager) Expiries() map[string]time.Time {
	m.mu.RLock()
	defer m.mu.RUnlock()
	expiries := make(map[string]time.Time, len(m.certs))
	for name, certificate := range m.certs {
		if certificate.Leaf != nil {
			expiries[name] = certificate.Leaf.NotAfter
		}
	}
	return expiries
}

// TLSConfig serves name's current certificate. The callback only reads the
// cache, so renewals swap certificates without touching the listener.
func (m *Manager) TLSConfig(name string) *tls.Config {
	name = strings.ToLower(strings.TrimSuffix(name, "."))
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		NextProtos: []string{"http/1.1"},
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			certificate, ok := m.cached(name)
			if !ok {
				return nil, fmt.Errorf("no certificate for %s", name)
			}
			return certificate, nil
		},
	}
}

func (m *Manager) cached(name string) (*tls.Certificate, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	certificate, ok := m.certs[name]
	return certificate, ok
}

func (m *Manager) remember(name string, certificate *tls.Certificate) {
	m.mu.Lock()
	m.certs[name] = certificate
	m.mu.Unlock()
}

func (m *Manager) nameLock(name string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	lock, ok := m.inflight[name]
	if !ok {
		lock = &sync.Mutex{}
		m.inflight[name] = lock
	}
	return lock
}

func (m *Manager) needsRenewal(certificate *tls.Certificate) bool {
	if certificate.Leaf == nil {
		return true
	}
	return m.config.Now().After(certificate.Leaf.NotAfter.Add(-renewBefore))
}

func (m *Manager) issueAndStore(ctx context.Context, name string) (*tls.Certificate, error) {
	certificatePEM, keyPEM, err := m.config.issue(ctx, name)
	if err != nil {
		return nil, err
	}
	if err := writeSecret(m.certificatePath(name), certificatePEM, 0o644); err != nil {
		return nil, err
	}
	if err := writeSecret(m.keyPath(name), keyPEM, 0o600); err != nil {
		return nil, err
	}
	certificate, err := parseCertificate(certificatePEM, keyPEM)
	if err != nil {
		return nil, err
	}
	m.remember(name, certificate)
	m.config.Logger.Info("issued certificate", "name", name, "not_after", certificate.Leaf.NotAfter.Format(time.RFC3339))
	return certificate, nil
}

func (m *Manager) load(name string) (*tls.Certificate, error) {
	certificatePEM, err := os.ReadFile(m.certificatePath(name))
	if err != nil {
		return nil, err
	}
	keyPEM, err := os.ReadFile(m.keyPath(name))
	if err != nil {
		return nil, err
	}
	return parseCertificate(certificatePEM, keyPEM)
}

func (m *Manager) certificatePath(name string) string {
	return filepath.Join(m.config.StateDir, name+".crt")
}

func (m *Manager) keyPath(name string) string {
	return filepath.Join(m.config.StateDir, name+".key")
}

func parseCertificate(certificatePEM, keyPEM []byte) (*tls.Certificate, error) {
	certificate, err := tls.X509KeyPair(certificatePEM, keyPEM)
	if err != nil {
		return nil, err
	}
	leaf, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil {
		return nil, err
	}
	certificate.Leaf = leaf
	return &certificate, nil
}

func writeSecret(path string, content []byte, mode os.FileMode) error {
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, content, mode); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

// accountKey loads or creates the ACME account key. Serialized so that
// concurrent first issuances for different names share one account.
func (m *Manager) accountKey() (*ecdsa.PrivateKey, error) {
	m.accountMu.Lock()
	defer m.accountMu.Unlock()
	path := filepath.Join(m.config.StateDir, "acme-account.key")
	if encoded, err := os.ReadFile(path); err == nil {
		block, _ := pem.Decode(encoded)
		if block == nil {
			return nil, fmt.Errorf("%s holds no PEM block", path)
		}
		return x509.ParseECPrivateKey(block.Bytes)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	encoded := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
	if err := writeSecret(path, encoded, 0o600); err != nil {
		return nil, err
	}
	return key, nil
}
