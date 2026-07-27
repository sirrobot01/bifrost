package home

import (
	"context"
	"errors"
	"net/netip"
	"slices"
	"testing"
	"time"

	"github.com/sirrobot01/bifrost/internal/dnspublish"
	"github.com/sirrobot01/bifrost/internal/exposure"
	"github.com/sirrobot01/bifrost/internal/netwatch"
	"github.com/sirrobot01/bifrost/internal/serviceaddr"
)

type fakeAddressManager struct {
	deriver serviceaddr.Deriver
	events  *[]string
	removed []netip.Addr
}

func (m *fakeAddressManager) Ensure(_ context.Context, prefix netip.Prefix, serviceID string) (serviceaddr.Lease, error) {
	address, err := m.deriver.Address(prefix, serviceID, 0)
	if err != nil {
		return serviceaddr.Lease{}, err
	}
	*m.events = append(*m.events, "address:"+address.String())
	return serviceaddr.Lease{Prefix: netip.PrefixFrom(address, 64)}, nil
}

func (m *fakeAddressManager) Remove(lease serviceaddr.Lease) error {
	m.removed = append(m.removed, lease.Prefix.Addr())
	*m.events = append(*m.events, "remove:"+lease.Prefix.Addr().String())
	return nil
}

type fakePublisher struct {
	events       *[]string
	publications []dnspublish.Publication
	withdrawn    []string
}

func (p *fakePublisher) Ensure(_ context.Context, publication dnspublish.Publication) error {
	publication.Addresses = append([]netip.Addr(nil), publication.Addresses...)
	p.publications = append(p.publications, publication)
	*p.events = append(*p.events, "dns:"+publication.Name)
	return nil
}

func (p *fakePublisher) Withdraw(_ context.Context, name string) error {
	p.withdrawn = append(p.withdrawn, name)
	*p.events = append(*p.events, "withdraw:"+name)
	return nil
}

func (p *fakePublisher) Prune(context.Context, []string) error {
	return nil
}

type fakeSplicer struct {
	address     netip.AddrPort
	shutdown    int
	shutdownErr error
}

func (s *fakeSplicer) Address() netip.AddrPort         { return s.address }
func (s *fakeSplicer) Status() exposure.Status         { return exposure.Status{} }
func (s *fakeSplicer) Serve(ctx context.Context) error { <-ctx.Done(); return nil }
func (s *fakeSplicer) Shutdown(context.Context) error  { s.shutdown++; return s.shutdownErr }

type controllerFixture struct {
	controller *Controller
	addresses  *fakeAddressManager
	publisher  *fakePublisher
	splicers   []*fakeSplicer
	events     []string
	now        time.Time
}

func newControllerFixture(t *testing.T) *controllerFixture {
	t.Helper()
	fixture := &controllerFixture{now: time.Date(2026, time.July, 25, 12, 0, 0, 0, time.UTC)}
	deriver, err := serviceaddr.NewDeriver(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	fixture.addresses = &fakeAddressManager{deriver: deriver, events: &fixture.events}
	fixture.publisher = &fakePublisher{events: &fixture.events}
	controller, err := NewController(ControllerConfig{
		Addresses: fixture.addresses,
		Deriver:   deriver,
		Publisher: fixture.publisher,
		Listen: func(_ context.Context, config exposure.Config) (Splicer, error) {
			fixture.events = append(fixture.events, "listen:"+config.ListenAddress.Addr().String())
			splicer := &fakeSplicer{address: config.ListenAddress}
			fixture.splicers = append(fixture.splicers, splicer)
			return splicer, nil
		},
		TTL:         60 * time.Second,
		DrainGrace:  time.Minute,
		DialTimeout: time.Second,
		IdleTimeout: time.Minute,
		Now:         func() time.Time { return fixture.now },
		CheckDirect: func(context.Context, netip.AddrPort) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture.controller = controller
	return fixture
}

func TestControllerPreparesSpliceBeforePublishing(t *testing.T) {
	t.Parallel()

	fixture := newControllerFixture(t)
	_, err := fixture.controller.Reconcile(t.Context(), []Service{spliceService()}, snapshot("2001:db8:1::10/64"), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(fixture.events) != 3 || fixture.events[0][:8] != "address:" || fixture.events[1][:7] != "listen:" || fixture.events[2] != "dns:media.example.com" {
		t.Fatalf("events = %v", fixture.events)
	}
	status := fixture.controller.Status()
	if len(status) != 1 || status[0].Mode != ModeSplice || status[0].ClientIPPreserved {
		t.Fatalf("status = %+v", status)
	}
}

func TestControllerShutdownToleratesDrainDeadline(t *testing.T) {
	t.Parallel()

	fixture := newControllerFixture(t)
	if _, err := fixture.controller.Reconcile(t.Context(), []Service{spliceService()}, snapshot("2001:db8:1::10/64"), false); err != nil {
		t.Fatal(err)
	}
	if len(fixture.splicers) != 1 {
		t.Fatalf("splicers = %d", len(fixture.splicers))
	}
	// A connection held past the grace period forces the splicer to cut it and
	// report the deadline. That is drain policy completing, not a failure.
	fixture.splicers[0].shutdownErr = errors.Join(nil, context.DeadlineExceeded)
	if err := fixture.controller.Shutdown(t.Context()); err != nil {
		t.Fatalf("drain deadline surfaced as shutdown failure: %v", err)
	}
	if len(fixture.publisher.withdrawn) != 1 || fixture.publisher.withdrawn[0] != "media.example.com" {
		t.Fatalf("withdrawn = %v", fixture.publisher.withdrawn)
	}
	if fixture.splicers[0].shutdown != 1 {
		t.Fatalf("splicer shutdown calls = %d", fixture.splicers[0].shutdown)
	}
}

func TestReconcileBackoffDoublesToCeiling(t *testing.T) {
	t.Parallel()

	settle := 10 * time.Second
	tests := map[int]time.Duration{
		1:  10 * time.Second,
		2:  20 * time.Second,
		4:  80 * time.Second,
		6:  320 * time.Second,
		7:  10 * time.Minute,
		50: 10 * time.Minute,
	}
	for failures, want := range tests {
		if got := reconcileBackoff(settle, failures); got != want {
			t.Errorf("reconcileBackoff(%v, %d) = %v, want %v", settle, failures, got, want)
		}
	}
}

func TestControllerOverlapsPrefixRotationThenDrains(t *testing.T) {
	t.Parallel()

	fixture := newControllerFixture(t)
	service := spliceService()
	if _, err := fixture.controller.Reconcile(t.Context(), []Service{service}, snapshot("2001:db8:1::10/64"), false); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.controller.Reconcile(t.Context(), []Service{service}, snapshot("2001:db8:2::10/64"), false); err != nil {
		t.Fatal(err)
	}
	latest := fixture.publisher.publications[len(fixture.publisher.publications)-1]
	if len(latest.Addresses) != 2 {
		t.Fatalf("overlap addresses = %v", latest.Addresses)
	}
	if fixture.splicers[0].shutdown != 0 {
		t.Fatal("old listener stopped before drain grace")
	}

	fixture.now = fixture.now.Add(time.Minute)
	if _, err := fixture.controller.Reconcile(t.Context(), []Service{service}, snapshot("2001:db8:2::10/64"), false); err != nil {
		t.Fatal(err)
	}
	latest = fixture.publisher.publications[len(fixture.publisher.publications)-1]
	if len(latest.Addresses) != 1 || fixture.splicers[0].shutdown != 1 || len(fixture.addresses.removed) != 1 {
		t.Fatalf("publication = %v, shutdown = %d, removed = %v", latest.Addresses, fixture.splicers[0].shutdown, fixture.addresses.removed)
	}
}

func TestControllerUsesDirectPathWithoutManagedResources(t *testing.T) {
	t.Parallel()

	fixture := newControllerFixture(t)
	address := netip.MustParseAddr("2001:db8:1::10")
	service := Service{ID: "media", DNSName: "media.example.com", Mode: ModeDirect, PublicAddress: address, ListenPort: 443, Backend: netip.AddrPortFrom(address, 443), MaxConnections: 1}
	if _, err := fixture.controller.Reconcile(t.Context(), []Service{service}, snapshot("2001:db8:1::10/64"), false); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(fixture.events, []string{"dns:media.example.com"}) {
		t.Fatalf("events = %v", fixture.events)
	}
	if status := fixture.controller.Status()[0]; status.Mode != ModeDirect || !status.ClientIPPreserved {
		t.Fatalf("status = %+v", status)
	}
}

func TestControllerDryRunDoesNotMutate(t *testing.T) {
	t.Parallel()

	fixture := newControllerFixture(t)
	actions, err := fixture.controller.Reconcile(t.Context(), []Service{spliceService()}, snapshot("2001:db8:1::10/64"), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(actions) != 2 || len(fixture.events) != 0 || len(fixture.controller.Status()) != 0 {
		t.Fatalf("actions = %v, events = %v, status = %v", actions, fixture.events, fixture.controller.Status())
	}
}

func spliceService() Service {
	return Service{ID: "media", DNSName: "media.example.com", Mode: ModeSplice, ListenPort: 443, Backend: netip.MustParseAddrPort("192.0.2.10:8096"), MaxConnections: 10}
}

func snapshot(address string) netwatch.Snapshot {
	return netwatch.Snapshot{InterfaceName: "eth0", MTU: 1500, Candidates: []serviceaddr.Candidate{{Prefix: netip.MustParsePrefix(address)}}}
}
