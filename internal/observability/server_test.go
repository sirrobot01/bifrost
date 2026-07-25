package observability

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
