package edge

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestEnrollmentRoundTrip(t *testing.T) {
	t.Parallel()

	original := Enrollment{Key: testKey, Allow: []string{"media.example.com", "photos.example.com"}}
	token, err := EncodeEnrollment(original)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(token, "bfe1.") {
		t.Fatalf("token = %q, want a versioned prefix", token)
	}
	// A token is pasted into a shell, so it must survive as one bare word.
	if strings.ContainsAny(token, " \t\n/+=") {
		t.Fatalf("token contains characters that break a shell paste: %q", token)
	}

	decoded, err := DecodeEnrollment(token)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Key != original.Key || strings.Join(decoded.Allow, ",") != strings.Join(original.Allow, ",") {
		t.Fatalf("decoded = %+v, want %+v", decoded, original)
	}
	// Operators copy tokens out of terminals, which adds whitespace.
	if _, err := DecodeEnrollment("  " + token + "\n"); err != nil {
		t.Fatalf("surrounding whitespace broke decoding: %v", err)
	}
}

func TestDecodeEnrollmentRejectsBadInput(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"not a token":     "hello",
		"wrong prefix":    "bfe9.aGVsbG8",
		"not base64":      "bfe1.!!!!",
		"not json":        "bfe1.aGVsbG8",
		"short key":       mustEncode(t, Enrollment{Key: "abcd", Allow: []string{"a.example.com"}}),
		"no allowed name": mustEncodeRaw(t, `{"k":"`+testKey+`","n":[]}`),
	}
	for name, token := range tests {
		if _, err := DecodeEnrollment(token); err == nil {
			t.Errorf("%s: decoded successfully, want an error", name)
		}
	}
}

func TestEnrollmentConfigYAMLLoads(t *testing.T) {
	t.Parallel()

	enrollment := Enrollment{Key: testKey, Allow: []string{"media.example.com", "photos.example.com"}}
	rendered := enrollment.ConfigYAML("/etc/bifrost/edge-key")
	path := writeTemp(t, rendered)
	configFile, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("generated edge config does not load: %v\n%s", err, rendered)
	}
	if len(configFile.Allow) != 2 || configFile.Allow[0] != "media.example.com" {
		t.Fatalf("allow = %v", configFile.Allow)
	}
	if configFile.KeyFile != "/etc/bifrost/edge-key" {
		t.Fatalf("key_file = %q", configFile.KeyFile)
	}
}

func mustEncode(t *testing.T, enrollment Enrollment) string {
	t.Helper()
	token, err := EncodeEnrollment(enrollment)
	if err != nil {
		// Encoding validates too, so an invalid fixture yields a token that
		// the decoder must still reject on its own.
		return mustEncodeRaw(t, `{"k":"`+enrollment.Key+`","n":["a.example.com"]}`)
	}
	return token
}

func mustEncodeRaw(t *testing.T, payload string) string {
	t.Helper()
	return tokenPrefix + base64.RawURLEncoding.EncodeToString([]byte(payload))
}

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "edge.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
