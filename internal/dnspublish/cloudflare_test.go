package dnspublish

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
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

// recordPage renders count records whose IDs continue from offset.
func recordPage(offset, count int, resultInfo string) string {
	records := make([]string, 0, count)
	for index := range count {
		records = append(records, fmt.Sprintf(
			`{"id":"record-%d","type":"TXT","name":"_bifrost.a%d.example.com","content":"owner","ttl":60}`,
			offset+index, offset+index))
	}
	body := `{"success":true,"errors":[],"result":[` + strings.Join(records, ",") + `]`
	if resultInfo != "" {
		body += `,"result_info":` + resultInfo
	}
	return body + `}`
}

// A zone larger than one page must be listed in full. Truncating it silently
// would make Prune miss ownership markers and strand the records they cover.
func TestCloudflareListZoneWalksEveryPage(t *testing.T) {
	t.Parallel()

	const total = 237
	var pagesSeen []string
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		query := request.URL.Query()
		mu.Lock()
		pagesSeen = append(pagesSeen, query.Get("page"))
		mu.Unlock()
		if got := query.Get("per_page"); got != "100" {
			t.Errorf("per_page = %q, want 100", got)
		}
		page, err := strconv.Atoi(query.Get("page"))
		if err != nil || page < 1 {
			t.Errorf("page = %q", query.Get("page"))
			return
		}
		offset := (page - 1) * 100
		count := min(100, max(0, total-offset))
		info := fmt.Sprintf(`{"page":%d,"per_page":100,"count":%d,"total_count":%d,"total_pages":3}`, page, count, total)
		_, _ = fmt.Fprint(response, recordPage(offset, count, info))
	}))
	defer server.Close()

	provider, err := NewCloudflare(CloudflareConfig{ZoneID: "zone", APIToken: "token", BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	records, err := provider.ListZone(t.Context(), RecordTXT)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != total {
		t.Fatalf("records = %d, want %d", len(records), total)
	}
	for index, record := range records {
		if want := fmt.Sprintf("record-%d", index); record.ID != want {
			t.Fatalf("records[%d].ID = %q, want %q", index, record.ID, want)
		}
	}
	if got := strings.Join(pagesSeen, ","); got != "1,2,3" {
		t.Fatalf("pages requested = %q, want 1,2,3", got)
	}
}

// When the record count is an exact multiple of the page size, the reported
// totals must end the walk rather than a wasted extra request.
func TestCloudflareListZoneStopsOnReportedTotals(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		page := requests.Add(1)
		if page > 2 {
			t.Errorf("requested page %d after the totals reported 2", page)
		}
		info := fmt.Sprintf(`{"page":%s,"per_page":100,"count":100,"total_count":200,"total_pages":2}`, request.URL.Query().Get("page"))
		_, _ = fmt.Fprint(response, recordPage(int(page-1)*100, 100, info))
	}))
	defer server.Close()

	provider, err := NewCloudflare(CloudflareConfig{ZoneID: "zone", APIToken: "token", BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	records, err := provider.ListZone(t.Context(), RecordTXT)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 200 {
		t.Fatalf("records = %d, want 200", len(records))
	}
	if requests.Load() != 2 {
		t.Fatalf("requests = %d, want 2", requests.Load())
	}
}

// result_info is optional, so a provider that omits it entirely must still be
// walked correctly off the short-page signal alone.
func TestCloudflareListZoneWalksWithoutResultInfo(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		switch requests.Add(1) {
		case 1:
			_, _ = fmt.Fprint(response, recordPage(0, 100, ""))
		case 2:
			_, _ = fmt.Fprint(response, recordPage(100, 100, ""))
		default:
			_, _ = fmt.Fprint(response, recordPage(0, 0, ""))
		}
	}))
	defer server.Close()

	provider, err := NewCloudflare(CloudflareConfig{ZoneID: "zone", APIToken: "token", BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	records, err := provider.ListZone(t.Context(), RecordTXT)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 200 {
		t.Fatalf("records = %d, want 200", len(records))
	}
	if requests.Load() != 3 {
		t.Fatalf("requests = %d, want 3 including the terminating empty page", requests.Load())
	}
}

// A provider that never stops handing back full pages must fail rather than
// loop forever holding the reconcile cycle open.
func TestCloudflareListZoneBoundsRunawayPagination(t *testing.T) {
	t.Parallel()

	page := recordPage(0, 100, "")
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = fmt.Fprint(response, page)
	}))
	defer server.Close()

	provider, err := NewCloudflare(CloudflareConfig{ZoneID: "zone", APIToken: "token", BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.ListZone(t.Context(), RecordTXT); err == nil {
		t.Fatal("ListZone succeeded against a provider that never ends pagination")
	}
	if got := requests.Load(); got != cloudflareMaxPages {
		t.Fatalf("requests = %d, want the %d page cap", got, cloudflareMaxPages)
	}
}

// A single short page must still cost exactly one request.
func TestCloudflareListSinglePageMakesOneRequest(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		if got := request.URL.Query().Get("page"); got != "1" {
			t.Errorf("page = %q, want 1", got)
		}
		_, _ = fmt.Fprint(response, recordPage(0, 3, `{"page":1,"per_page":100,"count":3,"total_count":3,"total_pages":1}`))
	}))
	defer server.Close()

	provider, err := NewCloudflare(CloudflareConfig{ZoneID: "zone", APIToken: "token", BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	records, err := provider.List(t.Context(), "photos.example.com", RecordTXT)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 3 {
		t.Fatalf("records = %d, want 3", len(records))
	}
	if requests.Load() != 1 {
		t.Fatalf("requests = %d, want 1", requests.Load())
	}
}

// Some responses carry a total without a page count, so the collected total has
// to end the walk on its own.
func TestCloudflareListZoneStopsOnTotalCountAlone(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		page := requests.Add(1)
		if page > 2 {
			t.Errorf("requested page %d after the total was reached", page)
		}
		_, _ = fmt.Fprint(response, recordPage(int(page-1)*100, 100, `{"per_page":100,"count":100,"total_count":200}`))
	}))
	defer server.Close()

	provider, err := NewCloudflare(CloudflareConfig{ZoneID: "zone", APIToken: "token", BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	records, err := provider.ListZone(t.Context(), RecordTXT)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 200 {
		t.Fatalf("records = %d, want 200", len(records))
	}
	if requests.Load() != 2 {
		t.Fatalf("requests = %d, want 2", requests.Load())
	}
}

func TestLookupCloudflareZonePrefersTheMostSpecificZone(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/zones" {
			t.Errorf("path = %q, want /zones", request.URL.Path)
		}
		_, _ = fmt.Fprint(response, `{"success":true,"errors":[],"result":[
			{"id":"apex","name":"example.com"},
			{"id":"delegated","name":"home.example.com"},
			{"id":"other","name":"example.net"}
		]}`)
	}))
	defer server.Close()

	zoneID, zoneName, err := LookupCloudflareZone(t.Context(), CloudflareConfig{APIToken: "token", BaseURL: server.URL}, "media.home.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if zoneID != "delegated" || zoneName != "home.example.com" {
		t.Fatalf("zone = %q/%q, want delegated/home.example.com", zoneID, zoneName)
	}
}

func TestLookupCloudflareZoneRejectsUnservedName(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(response, `{"success":true,"errors":[],"result":[{"id":"apex","name":"example.net"}]}`)
	}))
	defer server.Close()

	_, _, err := LookupCloudflareZone(t.Context(), CloudflareConfig{APIToken: "token", BaseURL: server.URL}, "media.example.com")
	if err == nil {
		t.Fatal("LookupCloudflareZone succeeded for a name no zone serves")
	}
	if !strings.Contains(err.Error(), "Zone:Read") {
		t.Fatalf("error = %q, want the token permission hint", err)
	}
}

func TestLookupCloudflareZoneRejectsSiblingSuffix(t *testing.T) {
	t.Parallel()

	// "notexample.com" ends with "example.com" as a string but is a different
	// zone, so suffix matching has to respect the label boundary.
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(response, `{"success":true,"errors":[],"result":[{"id":"apex","name":"example.com"}]}`)
	}))
	defer server.Close()

	if _, _, err := LookupCloudflareZone(t.Context(), CloudflareConfig{APIToken: "token", BaseURL: server.URL}, "notexample.com"); err == nil {
		t.Fatal("LookupCloudflareZone matched across a label boundary")
	}
}
