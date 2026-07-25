package diagnose

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func TestHTTPProber(t *testing.T) {
	t.Parallel()

	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			t.Errorf("method = %s", request.Method)
		}
		var probeRequest ProbeRequest
		if err := json.NewDecoder(request.Body).Decode(&probeRequest); err != nil {
			t.Error(err)
		}
		if probeRequest.Address != netip.MustParseAddr("2001:db8::1") || probeRequest.Port != 443 {
			t.Errorf("request = %+v", probeRequest)
		}
		_ = json.NewEncoder(writer).Encode(ProbeResult{Reachable: true, PathMTU: 1492, PacketTooBigWorks: true})
	}))
	defer server.Close()

	prober, err := NewHTTPProber(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	result, err := prober.Probe(context.Background(), ProbeRequest{Address: netip.MustParseAddr("2001:db8::1"), Port: 443})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Reachable || result.PathMTU != 1492 || !result.PacketTooBigWorks {
		t.Fatalf("result = %+v", result)
	}
}

func TestHTTPProberRequiresHTTPS(t *testing.T) {
	t.Parallel()

	if _, err := NewHTTPProber("http://probe.example.com", nil); err == nil || !strings.Contains(err.Error(), "HTTPS") {
		t.Fatalf("error = %v", err)
	}
}
