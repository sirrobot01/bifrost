package dnspublish

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestCloudflareList(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if got := request.Header.Get("Authorization"); got != "Bearer token" {
			t.Errorf("authorization = %q", got)
		}
		if got := request.URL.Query().Get("name"); got != "photos.example.com" {
			t.Errorf("name query = %q", got)
		}
		if got := request.URL.Query().Get("type"); got != "AAAA" {
			t.Errorf("type query = %q", got)
		}
		_, _ = fmt.Fprint(response, `{"success":true,"errors":[],"result":[{"id":"record-1","type":"AAAA","name":"photos.example.com","content":"2001:db8::1","ttl":60,"proxied":false}]}`)
	}))
	defer server.Close()

	provider, err := NewCloudflare(CloudflareConfig{ZoneID: "zone", APIToken: "token", BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	records, err := provider.List(t.Context(), "photos.example.com", RecordAAAA)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].ID != "record-1" {
		t.Fatalf("records = %+v", records)
	}
}

func TestCloudflareRetriesServerFailure(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			http.Error(response, "try again", http.StatusServiceUnavailable)
			return
		}
		_, _ = fmt.Fprint(response, `{"success":true,"errors":[],"result":[]}`)
	}))
	defer server.Close()

	provider, err := NewCloudflare(CloudflareConfig{ZoneID: "zone", APIToken: "token", BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	provider.baseDelay = time.Millisecond
	if _, err := provider.List(t.Context(), "photos.example.com", RecordAAAA); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 2 {
		t.Fatalf("requests = %d, want 2", requests.Load())
	}
}

func TestCloudflareRetryHonorsCancellation(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		http.Error(response, "try again", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	provider, err := NewCloudflare(CloudflareConfig{ZoneID: "zone", APIToken: "token", BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	provider.baseDelay = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := provider.List(ctx, "photos.example.com", RecordAAAA); err == nil {
		t.Fatal("List succeeded with cancelled context")
	}
}
