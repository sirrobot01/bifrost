package dnspublish

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"
)

type memoryProvider struct {
	records []Record
	events  []string
	nextID  int
}

func (p *memoryProvider) List(_ context.Context, name string, recordType RecordType) ([]Record, error) {
	var records []Record
	for _, record := range p.records {
		if record.Name == name && record.Type == recordType {
			records = append(records, record)
		}
	}
	return records, nil
}

func (p *memoryProvider) ListZone(_ context.Context, recordType RecordType) ([]Record, error) {
	var records []Record
	for _, record := range p.records {
		if record.Type == recordType {
			records = append(records, record)
		}
	}
	return records, nil
}

func (p *memoryProvider) Create(_ context.Context, record Record) (Record, error) {
	p.nextID++
	record.ID = fmt.Sprintf("record-%d", p.nextID)
	p.records = append(p.records, record)
	p.events = append(p.events, "create:"+string(record.Type)+":"+record.Content)
	return record, nil
}

func (p *memoryProvider) Update(_ context.Context, record Record) error {
	for i := range p.records {
		if p.records[i].ID == record.ID {
			p.records[i] = record
			p.events = append(p.events, "update:"+record.ID)
			return nil
		}
	}
	return errors.New("record not found")
}

func (p *memoryProvider) Delete(_ context.Context, id string) error {
	for i, record := range p.records {
		if record.ID == id {
			p.records = slices.Delete(p.records, i, i+1)
			p.events = append(p.events, "delete:"+id)
			return nil
		}
	}
	return errors.New("record not found")
}

func TestReconcilerEnsureCreatesOwnedPublication(t *testing.T) {
	t.Parallel()

	provider := &memoryProvider{}
	reconciler, err := NewReconciler(provider, "host-1")
	if err != nil {
		t.Fatal(err)
	}
	publication := Publication{
		Name: "Photos.Example.com.",
		Addresses: []netip.Addr{
			netip.MustParseAddr("2001:db8:1::20"),
			netip.MustParseAddr("2001:db8:1::10"),
			netip.MustParseAddr("2001:db8:1::20"),
		},
		TTL: time.Minute,
	}

	if err := reconciler.Ensure(t.Context(), publication); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"create:TXT:bifrost-owner=host-1",
		"create:AAAA:2001:db8:1::10",
		"create:AAAA:2001:db8:1::20",
	}
	if !slices.Equal(provider.events, want) {
		t.Fatalf("events = %v, want %v", provider.events, want)
	}
}

func TestReconcilerEnsureAddsBeforeRemovingDuringRotation(t *testing.T) {
	t.Parallel()

	provider := &memoryProvider{records: []Record{
		{ID: "owner", Type: RecordTXT, Name: "_bifrost.photos.example.com", Content: "bifrost-owner=host-1", TTL: 60},
		{ID: "old", Type: RecordAAAA, Name: "photos.example.com", Content: "2001:db8:1::10", TTL: 60},
	}}
	reconciler, err := NewReconciler(provider, "host-1")
	if err != nil {
		t.Fatal(err)
	}

	err = reconciler.Ensure(t.Context(), Publication{
		Name:      "photos.example.com",
		Addresses: []netip.Addr{netip.MustParseAddr("2001:db8:2::10")},
		TTL:       time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"create:AAAA:2001:db8:2::10", "delete:old"}
	if !slices.Equal(provider.events, want) {
		t.Fatalf("events = %v, want %v", provider.events, want)
	}
}

func TestReconcilerPublishesEdgeARecord(t *testing.T) {
	t.Parallel()

	provider := &memoryProvider{}
	reconciler, err := NewReconciler(provider, "host-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := reconciler.Ensure(t.Context(), Publication{
		Name:          "media.example.com",
		Addresses:     []netip.Addr{netip.MustParseAddr("2001:db8::1")},
		EdgeAddresses: []netip.Addr{netip.MustParseAddr("192.0.2.20")},
		TTL:           time.Minute,
	}); err != nil {
		t.Fatal(err)
	}
	want := []string{"create:TXT:bifrost-owner=host-1", "create:AAAA:2001:db8::1", "create:A:192.0.2.20"}
	if !slices.Equal(provider.events, want) {
		t.Fatalf("events = %v, want %v", provider.events, want)
	}
}

func TestReconcilerEnsureCorrectsRecordSettings(t *testing.T) {
	t.Parallel()

	provider := &memoryProvider{records: []Record{
		{ID: "owner", Type: RecordTXT, Name: "_bifrost.photos.example.com", Content: "bifrost-owner=host-1", TTL: 60},
		{ID: "address", Type: RecordAAAA, Name: "photos.example.com", Content: "2001:db8:1::10", TTL: 300, Proxied: true},
	}}
	reconciler, err := NewReconciler(provider, "host-1")
	if err != nil {
		t.Fatal(err)
	}

	err = reconciler.Ensure(t.Context(), Publication{
		Name:      "photos.example.com",
		Addresses: []netip.Addr{netip.MustParseAddr("2001:db8:1::10")},
		TTL:       time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(provider.events, []string{"update:address"}) {
		t.Fatalf("events = %v", provider.events)
	}
	if provider.records[1].TTL != 60 || provider.records[1].Proxied {
		t.Fatalf("record settings = %+v", provider.records[1])
	}
}

func TestReconcilerVerifyChecksOwnedStateWithoutMutation(t *testing.T) {
	t.Parallel()

	provider := &memoryProvider{records: []Record{
		{ID: "owner", Type: RecordTXT, Name: "_bifrost.media.example.com", Content: "bifrost-owner=host-1", TTL: 60},
		{ID: "ipv6", Type: RecordAAAA, Name: "media.example.com", Content: "2001:db8::10", TTL: 60},
		{ID: "ipv4", Type: RecordA, Name: "media.example.com", Content: "192.0.2.20", TTL: 60},
	}}
	reconciler, err := NewReconciler(provider, "host-1")
	if err != nil {
		t.Fatal(err)
	}
	publication := Publication{Name: "media.example.com", Addresses: []netip.Addr{netip.MustParseAddr("2001:db8::10")}, EdgeAddresses: []netip.Addr{netip.MustParseAddr("192.0.2.20")}, TTL: time.Minute}
	if err := reconciler.Verify(t.Context(), publication); err != nil {
		t.Fatal(err)
	}
	if len(provider.events) != 0 {
		t.Fatalf("provider was mutated: %v", provider.events)
	}
	provider.records[1].Content = "2001:db8::11"
	if err := reconciler.Verify(t.Context(), publication); err == nil {
		t.Fatal("Verify accepted drifted DNS state")
	}
}

func TestReconcilerPreflight(t *testing.T) {
	t.Parallel()

	clean, err := NewReconciler(&memoryProvider{}, "host-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := clean.Preflight(t.Context(), "media.example.com"); err != nil {
		t.Fatalf("clean zone failed preflight: %v", err)
	}

	owned, err := NewReconciler(&memoryProvider{records: []Record{
		{ID: "owner", Type: RecordTXT, Name: "_bifrost.media.example.com", Content: "bifrost-owner=host-1", TTL: 60},
		{ID: "ipv6", Type: RecordAAAA, Name: "media.example.com", Content: "2001:db8::10", TTL: 60},
	}}, "host-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := owned.Preflight(t.Context(), "media.example.com"); err != nil {
		t.Fatalf("owned zone failed preflight: %v", err)
	}

	foreign, err := NewReconciler(&memoryProvider{records: []Record{
		{ID: "manual", Type: RecordAAAA, Name: "media.example.com", Content: "2001:db8::99", TTL: 60},
	}}, "host-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := foreign.Preflight(t.Context(), "media.example.com"); err == nil || !strings.Contains(err.Error(), "ownership marker") {
		t.Fatalf("foreign records passed preflight: %v", err)
	}
}

func TestReconcilerVerifyReportsUnpublishedNameAsMissingRecords(t *testing.T) {
	t.Parallel()

	reconciler, err := NewReconciler(&memoryProvider{}, "host-1")
	if err != nil {
		t.Fatal(err)
	}
	err = reconciler.Verify(t.Context(), Publication{
		Name:      "media.example.com",
		Addresses: []netip.Addr{netip.MustParseAddr("2001:db8::10")},
		TTL:       time.Minute,
	})
	if err == nil {
		t.Fatal("Verify accepted an unpublished name")
	}
	if strings.Contains(err.Error(), "ownership marker") {
		t.Fatalf("unpublished name reported as an ownership conflict: %v", err)
	}
	if !strings.Contains(err.Error(), "record count is 0") {
		t.Fatalf("error does not name the missing records: %v", err)
	}
}

func TestReconcilerVerifyStillRefusesForeignRecords(t *testing.T) {
	t.Parallel()

	provider := &memoryProvider{records: []Record{
		{ID: "manual", Type: RecordAAAA, Name: "media.example.com", Content: "2001:db8::99", TTL: 60},
	}}
	reconciler, err := NewReconciler(provider, "host-1")
	if err != nil {
		t.Fatal(err)
	}
	err = reconciler.Verify(t.Context(), Publication{
		Name:      "media.example.com",
		Addresses: []netip.Addr{netip.MustParseAddr("2001:db8::99")},
		TTL:       time.Minute,
	})
	if err == nil || !strings.Contains(err.Error(), "ownership marker") {
		t.Fatalf("foreign records did not fail on ownership: %v", err)
	}
}

func TestReconcilerRefusesUnownedRecords(t *testing.T) {
	t.Parallel()

	provider := &memoryProvider{records: []Record{
		{ID: "manual", Type: RecordAAAA, Name: "photos.example.com", Content: "2001:db8:1::10", TTL: 60},
	}}
	reconciler, err := NewReconciler(provider, "host-1")
	if err != nil {
		t.Fatal(err)
	}

	err = reconciler.Ensure(t.Context(), Publication{
		Name:      "photos.example.com",
		Addresses: []netip.Addr{netip.MustParseAddr("2001:db8:2::10")},
		TTL:       time.Minute,
	})
	if err == nil {
		t.Fatal("Ensure replaced an unowned record")
	}
	if len(provider.events) != 0 {
		t.Fatalf("provider was mutated: %v", provider.events)
	}
}

func TestReconcilerWithdrawDeletesMarkerLast(t *testing.T) {
	t.Parallel()

	provider := &memoryProvider{records: []Record{
		{ID: "owner", Type: RecordTXT, Name: "_bifrost.photos.example.com", Content: "bifrost-owner=host-1", TTL: 60},
		{ID: "address-1", Type: RecordAAAA, Name: "photos.example.com", Content: "2001:db8:1::10", TTL: 60},
		{ID: "address-2", Type: RecordAAAA, Name: "photos.example.com", Content: "2001:db8:1::20", TTL: 60},
	}}
	reconciler, err := NewReconciler(provider, "host-1")
	if err != nil {
		t.Fatal(err)
	}

	if err := reconciler.Withdraw(t.Context(), "photos.example.com"); err != nil {
		t.Fatal(err)
	}
	want := []string{"delete:address-1", "delete:address-2", "delete:owner"}
	if !slices.Equal(provider.events, want) {
		t.Fatalf("events = %v, want %v", provider.events, want)
	}
}

func TestReconcilerPrunesOnlyOwnedOrphans(t *testing.T) {
	t.Parallel()

	provider := &memoryProvider{records: []Record{
		{ID: "kept-owner", Type: RecordTXT, Name: "_bifrost.keep.example.com", Content: "bifrost-owner=host-1", TTL: 60},
		{ID: "kept-address", Type: RecordAAAA, Name: "keep.example.com", Content: "2001:db8::1", TTL: 60},
		{ID: "orphan-owner", Type: RecordTXT, Name: "_bifrost.orphan.example.com", Content: "bifrost-owner=host-1", TTL: 60},
		{ID: "orphan-address", Type: RecordAAAA, Name: "orphan.example.com", Content: "2001:db8::2", TTL: 60},
		{ID: "other-owner", Type: RecordTXT, Name: "_bifrost.other.example.com", Content: "bifrost-owner=another-host", TTL: 60},
		{ID: "other-address", Type: RecordAAAA, Name: "other.example.com", Content: "2001:db8::3", TTL: 60},
	}}
	reconciler, err := NewReconciler(provider, "host-1")
	if err != nil {
		t.Fatal(err)
	}

	if err := reconciler.Prune(t.Context(), []string{"keep.example.com"}); err != nil {
		t.Fatal(err)
	}
	if got, want := provider.events, []string{"delete:orphan-address", "delete:orphan-owner"}; !slices.Equal(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
}
