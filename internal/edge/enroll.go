package edge

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// tokenPrefix versions the enrollment format. A future format can change the
// payload without a released edge misreading it.
const tokenPrefix = "bfe1."

// Enrollment is everything an edge host needs to serve a home deployment: the
// shared key that authenticates its metadata frames, and the names it may
// dispatch. Carrying the names removes the duplicate allowlist an operator
// would otherwise transcribe by hand.
type Enrollment struct {
	Key   string   `json:"k"`
	Allow []string `json:"n"`
}

// EncodeEnrollment renders an enrollment as a single copy-and-paste token.
// The token contains the shared key, so it is a secret in transit.
func EncodeEnrollment(enrollment Enrollment) (string, error) {
	if err := enrollment.validate(); err != nil {
		return "", err
	}
	payload, err := json.Marshal(enrollment)
	if err != nil {
		return "", err
	}
	return tokenPrefix + base64.RawURLEncoding.EncodeToString(payload), nil
}

// DecodeEnrollment parses a token produced by EncodeEnrollment.
func DecodeEnrollment(token string) (Enrollment, error) {
	token = strings.TrimSpace(token)
	if !strings.HasPrefix(token, tokenPrefix) {
		return Enrollment{}, fmt.Errorf("not a Bifrost edge token: expected it to start with %q", tokenPrefix)
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(token, tokenPrefix))
	if err != nil {
		return Enrollment{}, fmt.Errorf("edge token is not valid base64: %w", err)
	}
	var enrollment Enrollment
	if err := json.Unmarshal(payload, &enrollment); err != nil {
		return Enrollment{}, fmt.Errorf("edge token payload is not readable: %w", err)
	}
	if err := enrollment.validate(); err != nil {
		return Enrollment{}, err
	}
	return enrollment, nil
}

func (e Enrollment) validate() error {
	// The key is hex encoded and the verifier requires at least 32 bytes.
	if len(e.Key) < 64 {
		return errors.New("edge key must be at least 32 bytes, hex encoded")
	}
	if len(e.Allow) == 0 {
		return errors.New("an enrollment must allow at least one DNS name")
	}
	for _, name := range e.Allow {
		if strings.TrimSpace(name) == "" {
			return errors.New("an allowed DNS name is empty")
		}
	}
	return nil
}

// ConfigYAML renders the edge configuration for an enrollment, so the edge
// host never has to be told the allowlist twice.
func (e Enrollment) ConfigYAML(keyPath string) string {
	var builder strings.Builder
	builder.WriteString("version: 1\n")
	builder.WriteString("listen: :443\n")
	builder.WriteString("allow:\n")
	for _, name := range e.Allow {
		builder.WriteString("  - " + name + "\n")
	}
	builder.WriteString("key_file: " + keyPath + "\n")
	builder.WriteString("handshake_timeout: 5s\n")
	builder.WriteString("idle_timeout: 5m\n")
	builder.WriteString("max_connections: 1024\n")
	builder.WriteString("rate_per_minute: 120\n")
	builder.WriteString("rate_burst: 20\n")
	builder.WriteString("negative_ttl: 15s\n")
	builder.WriteString("stale_on_error: 30s\n")
	return builder.String()
}
