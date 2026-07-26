package dnspublish

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

func TestDESECProviderReadsAndReplacesRRSet(t *testing.T) {
	t.Parallel()

	rrset := desecRRSet{Subname: "media", Type: "AAAA", TTL: 60, Records: []string{"2001:db8::1"}}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Token token" {
			t.Errorf("authorization = %q", request.Header.Get("Authorization"))
		}
		switch request.Method {
		case http.MethodGet:
			_ = json.NewEncoder(writer).Encode(rrset)
		case http.MethodPatch:
			var payload []desecRRSet
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				t.Error(err)
			}
			rrset = payload[0]
			writer.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("method = %s", request.Method)
		}
	}))
	defer server.Close()

	provider, err := NewDESEC(DESECConfig{Zone: "example.com", Token: "token", BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	records, err := provider.List(t.Context(), "media.example.com", RecordAAAA)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Content != "2001:db8::1" {
		t.Fatalf("records = %+v", records)
	}
	if _, err := provider.Create(t.Context(), Record{Name: "media.example.com", Type: RecordAAAA, Content: "2001:db8::2", TTL: 60}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(rrset.Records, []string{"2001:db8::1", "2001:db8::2"}) {
		t.Fatalf("RRset = %+v", rrset)
	}
}

func TestLookupDESECZonePicksLongestCoveringDomain(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/domains/" {
			t.Errorf("path = %s", request.URL.Path)
		}
		_, _ = writer.Write([]byte(`[{"name":"dedyn.io.example"},{"name":"sirrobot01.dedyn.io"},{"name":"other.net"}]`))
	}))
	defer server.Close()

	zone, err := LookupDESECZone(t.Context(), DESECConfig{Token: "token", BaseURL: server.URL}, "plex.sirrobot01.dedyn.io")
	if err != nil {
		t.Fatal(err)
	}
	if zone != "sirrobot01.dedyn.io" {
		t.Fatalf("zone = %q", zone)
	}
	_, err = LookupDESECZone(t.Context(), DESECConfig{Token: "token", BaseURL: server.URL}, "plex.elsewhere.example")
	if err == nil {
		t.Fatal("uncovered name did not error")
	}
}

func TestDESECProviderExplainsMinimumTTLRejection(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet {
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		writer.WriteHeader(http.StatusBadRequest)
		_, _ = writer.Write([]byte(`[{"ttl":["Ensure this value is greater than or equal to 3600."]}]`))
	}))
	defer server.Close()

	provider, err := NewDESEC(DESECConfig{Zone: "example.com", Token: "token", BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.Create(t.Context(), Record{Name: "media.example.com", Type: RecordAAAA, Content: "2001:db8::1", TTL: 60})
	if err == nil {
		t.Fatal("minimum-TTL rejection did not error")
	}
	if !strings.Contains(err.Error(), "dns.ttl") || !strings.Contains(err.Error(), "minimum TTL") {
		t.Fatalf("error lacks remediation guidance: %v", err)
	}
}

func TestDESECProviderQuotesTXTRecords(t *testing.T) {
	t.Parallel()

	var written desecRRSet
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet {
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		var payload []desecRRSet
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		written = payload[0]
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	provider, err := NewDESEC(DESECConfig{Zone: "example.com", Token: "token", BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Create(t.Context(), Record{Name: "_bifrost.media.example.com", Type: RecordTXT, Content: "bifrost-owner=home", TTL: 60}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(written.Records, []string{`"bifrost-owner=home"`}) {
		t.Fatalf("records = %q", written.Records)
	}
}
