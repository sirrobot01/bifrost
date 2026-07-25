package dnspublish

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDynv6ProviderResolvesZoneAndManagesRecords(t *testing.T) {
	t.Parallel()

	records := []dynv6Record{{ID: 1, ZoneID: 42, Name: "media.example.com", Type: "AAAA", Data: "2001:db8::1"}}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer token" {
			t.Errorf("authorization = %q", request.Header.Get("Authorization"))
		}
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/zones/by-name/example.com":
			_ = json.NewEncoder(writer).Encode(dynv6Zone{ID: 42, Name: "example.com"})
		case request.Method == http.MethodGet:
			_ = json.NewEncoder(writer).Encode(records)
		case request.Method == http.MethodPost:
			var record dynv6Record
			if err := json.NewDecoder(request.Body).Decode(&record); err != nil {
				t.Error(err)
			}
			record.ID = 2
			record.ZoneID = 42
			records = append(records, record)
			_ = json.NewEncoder(writer).Encode(record)
		default:
			t.Errorf("request = %s %s", request.Method, request.URL.Path)
		}
	}))
	defer server.Close()

	provider, err := NewDynv6(Dynv6Config{Zone: "example.com", Token: "token", BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	listed, err := provider.List(t.Context(), "media.example.com", RecordAAAA)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].TTL != 0 {
		t.Fatalf("records = %+v", listed)
	}
	created, err := provider.Create(t.Context(), Record{Name: "photos.example.com", Type: RecordAAAA, Content: "2001:db8::2", TTL: 60})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID != "2" || created.Content != "2001:db8::2" {
		t.Fatalf("created = %+v", created)
	}
}
