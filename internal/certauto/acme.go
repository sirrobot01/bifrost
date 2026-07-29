package certauto

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"golang.org/x/crypto/acme"

	"github.com/sirrobot01/bifrost/internal/dnsprobe"
	"github.com/sirrobot01/bifrost/internal/dnspublish"
)

const letsEncryptDirectory = "https://acme-v02.api.letsencrypt.org/directory"

// acmeIssue obtains one certificate through a DNS-01 challenge. The challenge
// TXT record is created through the configured DNS provider and confirmed
// against the zone's authoritative nameservers before the CA is asked to
// validate, because the CA's resolvers see the record no sooner than the
// nameservers serve it.
func (m *Manager) acmeIssue(ctx context.Context, name string) ([]byte, []byte, error) {
	accountKey, err := m.accountKey()
	if err != nil {
		return nil, nil, fmt.Errorf("ACME account key: %w", err)
	}
	directory := m.config.DirectoryURL
	if directory == "" {
		directory = letsEncryptDirectory
	}
	client := &acme.Client{Key: accountKey, DirectoryURL: directory}
	account := &acme.Account{}
	if m.config.Email != "" {
		account.Contact = []string{"mailto:" + m.config.Email}
	}
	if _, err := client.Register(ctx, account, acme.AcceptTOS); err != nil && !errors.Is(err, acme.ErrAccountAlreadyExists) {
		return nil, nil, fmt.Errorf("register ACME account: %w", err)
	}

	order, err := client.AuthorizeOrder(ctx, acme.DomainIDs(name))
	if err != nil {
		return nil, nil, fmt.Errorf("create ACME order: %w", err)
	}
	for _, authorizationURL := range order.AuthzURLs {
		if err := m.solveAuthorization(ctx, client, name, authorizationURL); err != nil {
			return nil, nil, err
		}
	}

	certificateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	request, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject:  pkix.Name{CommonName: name},
		DNSNames: []string{name},
	}, certificateKey)
	if err != nil {
		return nil, nil, err
	}
	chain, _, err := client.CreateOrderCert(ctx, order.FinalizeURL, request, true)
	if err != nil {
		return nil, nil, fmt.Errorf("finalize ACME order: %w", err)
	}

	var certificatePEM []byte
	for _, der := range chain {
		certificatePEM = append(certificatePEM, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})...)
	}
	keyDER, err := x509.MarshalECPrivateKey(certificateKey)
	if err != nil {
		return nil, nil, err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return certificatePEM, keyPEM, nil
}

func (m *Manager) solveAuthorization(ctx context.Context, client *acme.Client, name, authorizationURL string) error {
	authorization, err := client.GetAuthorization(ctx, authorizationURL)
	if err != nil {
		return fmt.Errorf("read ACME authorization: %w", err)
	}
	if authorization.Status == acme.StatusValid {
		return nil
	}
	var challenge *acme.Challenge
	for _, candidate := range authorization.Challenges {
		if candidate.Type == "dns-01" {
			challenge = candidate
			break
		}
	}
	if challenge == nil {
		return errors.New("the CA offered no dns-01 challenge")
	}
	value, err := client.DNS01ChallengeRecord(challenge.Token)
	if err != nil {
		return err
	}
	// RFC 8555: the challenge for a wildcard lives at the parent's record name,
	// with no asterisk in it.
	challengeName := "_acme-challenge." + strings.TrimPrefix(name, "*.")
	record, err := m.config.Provider.Create(ctx, dnspublish.Record{
		Type:    dnspublish.RecordTXT,
		Name:    challengeName,
		Content: value,
		TTL:     int(m.config.ChallengeTTL.Seconds()),
	})
	if err != nil {
		return fmt.Errorf("create challenge record: %w", err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if err := m.config.Provider.Delete(cleanup, record.ID); err != nil {
			m.config.Logger.Warn("challenge record cleanup failed", "name", challengeName, "error", err)
		}
	}()
	if err := m.waitForChallengeRecord(ctx, challengeName, value); err != nil {
		return err
	}
	if _, err := client.Accept(ctx, challenge); err != nil {
		return fmt.Errorf("accept ACME challenge: %w", err)
	}
	if _, err := client.WaitAuthorization(ctx, authorization.URI); err != nil {
		return fmt.Errorf("ACME validation failed: %w", err)
	}
	return nil
}

// waitForChallengeRecord polls the zone's authoritative nameservers until the
// challenge value is served. Providers publish asynchronously; deSEC has been
// observed taking over a minute.
func (m *Manager) waitForChallengeRecord(ctx context.Context, name, value string) error {
	deadline := time.NewTimer(3 * time.Minute)
	defer deadline.Stop()
	interval := time.NewTicker(5 * time.Second)
	defer interval.Stop()
	for {
		values, _, err := dnsprobe.LookupTXT(ctx, name)
		if err == nil && slices.Contains(values, value) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("challenge record for %s was not served by the authoritative nameservers within 3 minutes", name)
		case <-interval.C:
		}
	}
}
