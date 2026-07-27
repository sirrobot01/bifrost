package dockerwatch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sirrobot01/bifrost/internal/config"
)

func TestClientListsEnabledContainers(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !strings.HasSuffix(request.URL.Path, "/containers/json") {
			t.Errorf("path = %s", request.URL.Path)
		}
		containers := []container{
			{
				ID:     "enabled",
				Names:  []string{"/media"},
				Labels: map[string]string{"bifrost.enable": "true", "bifrost.port": "8096", "bifrost.listen": "443", "bifrost.dns": "media.example.com", "bifrost.mode": "splice"},
				NetworkSettings: struct {
					Networks map[string]network `json:"Networks"`
				}{Networks: map[string]network{"frontend": {IPAddress: "172.20.0.2"}}},
			},
			{ID: "disabled", Labels: map[string]string{}},
		}
		_ = json.NewEncoder(writer).Encode(containers)
	}))
	defer server.Close()

	client, err := NewClient(ClientConfig{HTTPClient: server.Client(), BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	services, err := client.ListServices(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(services) != 1 || services[0].Name != "media" || services[0].Backend != "172.20.0.2:8096" || services[0].Listen != 443 {
		t.Fatalf("services = %+v", services)
	}
}

func TestServiceFromContainerReadsTLSLabel(t *testing.T) {
	t.Parallel()

	build := func(labels map[string]string) config.StaticService {
		t.Helper()
		labels["bifrost.port"] = "8096"
		labels["bifrost.dns"] = "media.example.com"
		labels["bifrost.mode"] = "splice"
		subject := container{ID: "container", Names: []string{"/media"}, Labels: labels}
		subject.NetworkSettings.Networks = map[string]network{"frontend": {IPAddress: "172.20.0.2"}}
		service, err := serviceFromContainer(subject)
		if err != nil {
			t.Fatal(err)
		}
		return service
	}

	// Default-on matches static services.
	if service := build(map[string]string{}); !service.TerminatesTLS() {
		t.Fatalf("tls = %q, want termination by default", service.TLS)
	}
	// A containerized backend that speaks TLS itself must be able to opt out.
	if service := build(map[string]string{"bifrost.tls": "off"}); service.TerminatesTLS() {
		t.Fatalf("tls = %q, want passthrough", service.TLS)
	}
	subject := container{ID: "container", Names: []string{"/media"}, Labels: map[string]string{"bifrost.port": "8096", "bifrost.dns": "media.example.com", "bifrost.tls": "maybe"}}
	subject.NetworkSettings.Networks = map[string]network{"frontend": {IPAddress: "172.20.0.2"}}
	if _, err := serviceFromContainer(subject); err == nil {
		t.Fatal("an unsupported bifrost.tls value was accepted")
	}
}

func TestClientRequiresNetworkSelection(t *testing.T) {
	t.Parallel()

	container := container{ID: "container", Names: []string{"/media"}, Labels: map[string]string{"bifrost.port": "8096", "bifrost.dns": "media.example.com"}}
	container.NetworkSettings.Networks = map[string]network{"first": {IPAddress: "172.20.0.2"}, "second": {IPAddress: "172.21.0.2"}}
	if _, err := serviceFromContainer(container); err == nil || !strings.Contains(err.Error(), "bifrost.network") {
		t.Fatalf("error = %v", err)
	}
}

func TestClientWatchesContainerEvents(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		flusher := writer.(http.Flusher)
		_, _ = writer.Write([]byte("{\"Type\":\"container\",\"Action\":\"start\"}\n"))
		flusher.Flush()
		<-request.Context().Done()
	}))
	defer server.Close()
	client, err := NewClient(ClientConfig{HTTPClient: server.Client(), BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	changes := make(chan struct{}, 1)
	result := make(chan error, 1)
	go func() { result <- client.Watch(ctx, changes) }()
	<-changes
	cancel()
	if err := <-result; err == nil || !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("error = %v", err)
	}
}
