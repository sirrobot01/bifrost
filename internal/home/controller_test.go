package home

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"github.com/sirrobot01/bifrost/internal/edgeauth"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sirrobot01/bifrost/internal/dnspublish"
	"github.com/sirrobot01/bifrost/internal/exposure"
	"github.com/sirrobot01/bifrost/internal/hostfw"
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

type fakeCertificates struct {
	failing map[string]error
	issued  []string
	mu      sync.Mutex
}

func (f *fakeCertificates) Certificate(_ context.Context, name string) (*tls.Certificate, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err, exists := f.failing[name]; exists {
		return nil, err
	}
	f.issued = append(f.issued, name)
	return &tls.Certificate{}, nil
}

func (f *fakeCertificates) TLSConfig(string) *tls.Config { return &tls.Config{} }

type fakeFirewall struct {
	events   *[]string
	applied  []hostfw.Spec
	removed  int
	applyErr error
}

func (f *fakeFirewall) Apply(_ context.Context, spec hostfw.Spec) error {
	if f.applyErr != nil {
		return f.applyErr
	}
	f.applied = append(f.applied, spec)
	*f.events = append(*f.events, "firewall:"+spec.Describe())
	return nil
}

func (f *fakeFirewall) Remove(context.Context) error {
	f.removed++
	return nil
}

func TestControllerOpensFirewallBeforePublishing(t *testing.T) {
	t.Parallel()

	fixture := newControllerFixture(t)
	firewall := &fakeFirewall{events: &fixture.events}
	fixture.controller.config.Firewall = firewall
	fixture.controller.config.FirewallAllowances = hostfw.Spec{TrustedInterfaces: []string{"tailscale0"}, AllowPorts: []uint16{22}}

	if _, err := fixture.controller.Reconcile(t.Context(), []Service{spliceService()}, snapshot("2001:db8:1::10/64"), false); err != nil {
		t.Fatal(err)
	}
	firewallIndex := slices.IndexFunc(fixture.events, func(event string) bool { return strings.HasPrefix(event, "firewall:") })
	publishIndex := slices.Index(fixture.events, "dns:media.example.com")
	if firewallIndex < 0 || publishIndex < 0 || firewallIndex > publishIndex {
		t.Fatalf("firewall must open before publication; events = %v", fixture.events)
	}
	if len(firewall.applied) != 1 {
		t.Fatalf("applied %d specs", len(firewall.applied))
	}
	spec := firewall.applied[0]
	if len(spec.Endpoints) != 1 || spec.Endpoints[0].Port != 443 {
		t.Fatalf("endpoints = %+v", spec.Endpoints)
	}
	if !slices.Equal(spec.AllowPorts, []uint16{22}) || !slices.Equal(spec.TrustedInterfaces, []string{"tailscale0"}) {
		t.Fatalf("allowances lost: %+v", spec)
	}

	// An unchanged policy must not be rewritten on every reconcile.
	if _, err := fixture.controller.Reconcile(t.Context(), []Service{spliceService()}, snapshot("2001:db8:1::10/64"), false); err != nil {
		t.Fatal(err)
	}
	if len(firewall.applied) != 1 {
		t.Fatalf("unchanged policy rewritten %d times", len(firewall.applied))
	}

	// A clean stop reverts the host to its pre-Bifrost policy.
	if err := fixture.controller.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	if firewall.removed != 1 {
		t.Fatalf("managed table removed %d times on shutdown", firewall.removed)
	}
}

func TestControllerOpensFirewallForDirectServices(t *testing.T) {
	t.Parallel()

	fixture := newControllerFixture(t)
	firewall := &fakeFirewall{events: &fixture.events}
	fixture.controller.config.Firewall = firewall
	address := netip.MustParseAddr("2001:db8:1::10")
	// Direct mode runs no Bifrost listener, but the backend owns the address
	// and still needs its port opened, or managed mode black-holes it.
	service := Service{ID: "media", DNSName: "media.example.com", Mode: ModeDirect, PublicAddress: address, ListenPort: 443, Backend: netip.AddrPortFrom(address, 443), MaxConnections: 1}
	if _, err := fixture.controller.Reconcile(t.Context(), []Service{service}, snapshot("2001:db8:1::10/64"), false); err != nil {
		t.Fatal(err)
	}
	if len(firewall.applied) != 1 {
		t.Fatalf("applied %d specs", len(firewall.applied))
	}
	endpoints := firewall.applied[0].Endpoints
	if len(endpoints) != 1 || endpoints[0].Address != address || endpoints[0].Port != 443 {
		t.Fatalf("direct service missing from the firewall policy: %+v", endpoints)
	}
}

func TestAutoModeRejectsDirectWhenPortsDiffer(t *testing.T) {
	t.Parallel()

	// A backend on 0.0.0.0:32400 published on 443 can only work through a
	// splice. Selecting direct would resolve the name to an address whose
	// port nothing serves, and a probe can be fooled by a previous run's own
	// listener, so the port mismatch must rule direct out first.
	service := Service{ID: "plex", DNSName: "plex.example.com", Mode: ModeAuto, ListenPort: 443, Backend: netip.MustParseAddrPort("0.0.0.0:32400"), MaxConnections: 1}
	_, err := directAddress(service, snapshot("2001:db8:1::10/64"), serviceaddr.Selection{Prefix: netip.MustParsePrefix("2001:db8:1::/64")}, nil, time.Now())
	if err == nil {
		t.Fatal("direct mode was selected for a backend whose port differs from the public port")
	}
	if !strings.Contains(err.Error(), "backend port") {
		t.Fatalf("err = %v, want the port mismatch named", err)
	}
}

func TestControllerPublishesDespiteFirewallFailure(t *testing.T) {
	t.Parallel()

	fixture := newControllerFixture(t)
	fixture.controller.config.Firewall = &fakeFirewall{events: &fixture.events, applyErr: errors.New("netlink refused")}

	_, err := fixture.controller.Reconcile(t.Context(), []Service{spliceService()}, snapshot("2001:db8:1::10/64"), false)
	if err == nil || !strings.Contains(err.Error(), "managed firewall") {
		t.Fatalf("err = %v, want the firewall failure reported", err)
	}
	// Another firewall may already permit the service, so the publication
	// still happens and the error only drives the retry.
	if !slices.Contains(fixture.events, "dns:media.example.com") {
		t.Fatalf("service was not published; events = %v", fixture.events)
	}
}

func TestControllerIsolatesCertificateFailures(t *testing.T) {
	t.Parallel()

	fixture := newControllerFixture(t)
	certificates := &fakeCertificates{failing: map[string]error{"broken.example.com": errors.New("CA said no")}}
	fixture.controller.config.Certificates = certificates

	healthy := spliceService()
	healthy.TLS = true
	broken := Service{ID: "broken", DNSName: "broken.example.com", Mode: ModeSplice, ListenPort: 443, Backend: netip.MustParseAddrPort("192.0.2.11:8096"), MaxConnections: 10, TLS: true}

	actions, err := fixture.controller.Reconcile(t.Context(), []Service{healthy, broken}, snapshot("2001:db8:1::10/64"), false)
	if err == nil || !strings.Contains(err.Error(), `service "broken"`) {
		t.Fatalf("err = %v, want the broken service named", err)
	}
	// The healthy service must be fully published despite the neighbor's
	// certificate failure.
	published := false
	for _, action := range actions {
		if action.Service == "media" && action.Kind == "publish" {
			published = true
		}
	}
	if !published {
		t.Fatalf("healthy service was not published; actions = %v", actions)
	}
	if len(fixture.publisher.publications) != 1 || fixture.publisher.publications[0].Name != "media.example.com" {
		t.Fatalf("publications = %+v", fixture.publisher.publications)
	}
}

func snapshot(address string) netwatch.Snapshot {
	return netwatch.Snapshot{InterfaceName: "eth0", MTU: 1500, Candidates: []serviceaddr.Candidate{{Prefix: netip.MustParsePrefix(address)}}}
}

type fakePinholes struct {
	applied  []hostfw.Spec
	released int
}

func (f *fakePinholes) Apply(_ context.Context, spec hostfw.Spec) {
	f.applied = append(f.applied, spec)
}

func (f *fakePinholes) Release(context.Context) { f.released++ }

// The router is asked for the same sockets the host firewall opened, and a
// clean stop closes what was opened.
func TestControllerRequestsRouterPinholes(t *testing.T) {
	t.Parallel()

	fixture := newControllerFixture(t)
	pinholes := &fakePinholes{}
	fixture.controller.config.Firewall = &fakeFirewall{events: &fixture.events}
	fixture.controller.config.Pinholes = pinholes

	if _, err := fixture.controller.Reconcile(t.Context(), []Service{spliceService()}, snapshot("2001:db8:1::10/64"), false); err != nil {
		t.Fatal(err)
	}
	if len(pinholes.applied) != 1 || len(pinholes.applied[0].Endpoints) != 1 {
		t.Fatalf("pinhole requests = %+v", pinholes.applied)
	}
	if pinholes.applied[0].Endpoints[0].Port != 443 {
		t.Fatalf("requested port = %d", pinholes.applied[0].Endpoints[0].Port)
	}
	if err := fixture.controller.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	if pinholes.released != 1 {
		t.Fatalf("released %d times, want 1", pinholes.released)
	}
}

// An operator with viewers in several regions puts an edge near each, so every
// edge address is published for the service rather than only the first.
func TestControllerPublishesEveryEdgeAddress(t *testing.T) {
	t.Parallel()

	fixture := newControllerFixture(t)
	fixture.controller.config.EdgeVerifier = testEdgeVerifier(t)
	service := spliceService()
	service.Edge = true
	service.EdgeAddresses = []netip.Addr{netip.MustParseAddr("192.0.2.10"), netip.MustParseAddr("198.51.100.20")}

	if _, err := fixture.controller.Reconcile(t.Context(), []Service{service}, snapshot("2001:db8:1::10/64"), false); err != nil {
		t.Fatal(err)
	}
	published := fixture.publisher.publications[len(fixture.publisher.publications)-1]
	if len(published.EdgeAddresses) != 2 {
		t.Fatalf("edge addresses = %v, want both published", published.EdgeAddresses)
	}

	// An unchanged service must not look changed just because a slice is
	// compared by identity somewhere.
	before := len(fixture.publisher.publications)
	if _, err := fixture.controller.Reconcile(t.Context(), []Service{service}, snapshot("2001:db8:1::10/64"), false); err != nil {
		t.Fatal(err)
	}
	if len(fixture.publisher.publications) != before {
		t.Fatal("an unchanged service was republished")
	}
}

func testEdgeVerifier(t *testing.T) *edgeauth.Verifier {
	t.Helper()
	verifier, err := edgeauth.NewVerifier(bytes.Repeat([]byte{0x42}, 32), 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return verifier
}
