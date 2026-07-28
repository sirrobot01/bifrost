package observability

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestServerReportsReadinessAndMetrics(t *testing.T) {
	t.Parallel()

	server, err := NewServer("127.0.0.1:0", func() Snapshot {
		return Snapshot{Ready: true, Services: []Service{{ID: "media", Mode: "splice", ActiveConnections: 2, Accepted: 4}}}
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.handler())
	defer httpServer.Close()

	for _, path := range []string{"/healthz", "/status", "/metrics"} {
		response, err := http.Get(httpServer.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, readErr := io.ReadAll(response.Body)
		closeErr := response.Body.Close()
		if readErr != nil || closeErr != nil {
			t.Fatal(readErr, closeErr)
		}
		if response.StatusCode != http.StatusOK {
			t.Fatalf("%s status = %d", path, response.StatusCode)
		}
		if path == "/metrics" && (!strings.Contains(string(body), "bifrost_ready 1") || !strings.Contains(string(body), `service="media"`)) {
			t.Fatalf("metrics = %s", body)
		}
	}
}

func TestServerIsUnreadyBeforeReconcile(t *testing.T) {
	t.Parallel()

	server, err := NewServer("127.0.0.1:0", func() Snapshot { return Snapshot{} })
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	response := httptest.NewRecorder()
	server.handler().ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", response.Code)
	}
}

// A renewal that has been failing is invisible until a handshake breaks, so
// expiry belongs in metrics where monitoring can alert on it.
func TestMetricsExposeCertificateExpiry(t *testing.T) {
	t.Parallel()

	expiry := time.Date(2026, time.October, 1, 12, 0, 0, 0, time.UTC)
	rendered := metrics(Snapshot{
		Ready:        true,
		Certificates: []Certificate{{Name: "media.example.com", NotAfter: expiry}},
	})
	want := `bifrost_certificate_expiry_seconds{name="media.example.com"} ` + strconv.FormatInt(expiry.Unix(), 10)
	if !strings.Contains(rendered, want) {
		t.Fatalf("metrics lack the certificate gauge:\n%s", rendered)
	}
	if !strings.Contains(rendered, "# TYPE bifrost_certificate_expiry_seconds gauge") {
		t.Fatalf("gauge is missing its TYPE line:\n%s", rendered)
	}
	// A deployment with no managed certificates should not emit an empty family.
	if strings.Contains(metrics(Snapshot{Ready: true}), "bifrost_certificate_expiry_seconds") {
		t.Fatal("the certificate family appears with no certificates")
	}
}
