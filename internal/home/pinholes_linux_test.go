//go:build linux

package home

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/netip"
	"testing"
	"time"

	"github.com/sirrobot01/bifrost/internal/hostfw"
	"github.com/sirrobot01/bifrost/internal/pcp"
)

type fakePCPRequester struct {
	requested  []pcp.Mapping
	released   []pcp.Mapping
	lifetime   time.Duration
	requestErr error
}

func (f *fakePCPRequester) Request(_ context.Context, mapping pcp.Mapping) (time.Duration, error) {
	f.requested = append(f.requested, mapping)
	if f.requestErr != nil {
		return 0, f.requestErr
	}
	return f.lifetime, nil
}

func (f *fakePCPRequester) Release(_ context.Context, mapping pcp.Mapping) error {
	f.released = append(f.released, mapping)
	return nil
}

func TestPinholeManagerRetriesLostRenewalWithoutDisablingPCP(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 28, 12, 0, 0, 0, time.UTC)
	client := &fakePCPRequester{lifetime: 20 * time.Minute}
	manager := &pinholeManager{
		client:  client,
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		now:     func() time.Time { return now },
		granted: make(map[netip.AddrPort]pinholeLease),
	}
	endpoint := hostfw.Endpoint{Address: netip.MustParseAddr("2001:db8::10"), Port: 443}
	manager.Ensure(t.Context(), endpoint)

	now = now.Add(10 * time.Minute)
	client.requestErr = testUnsupportedError()
	manager.Renew(t.Context())
	if manager.unsupported {
		t.Fatal("one lost renewal disabled a previously working PCP server")
	}

	now = now.Add(time.Second)
	manager.Renew(t.Context())
	if len(client.requested) != 2 {
		t.Fatalf("renewal retried on every sweep: %d requests", len(client.requested))
	}

	now = now.Add(time.Minute)
	client.requestErr = nil
	manager.Renew(t.Context())
	if len(client.requested) != 3 {
		t.Fatalf("bounded retry did not run: %d requests", len(client.requested))
	}
}

func testUnsupportedError() error {
	return errors.Join(pcp.ErrUnsupported, errors.New("test timeout"))
}

func TestPinholeManagerRenewsAtHalfGrantedLifetime(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 28, 12, 0, 0, 0, time.UTC)
	client := &fakePCPRequester{lifetime: 20 * time.Minute}
	manager := &pinholeManager{
		client:  client,
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		secret:  []byte("test secret"),
		now:     func() time.Time { return now },
		granted: make(map[netip.AddrPort]pinholeLease),
	}
	endpoint := hostfw.Endpoint{Service: "media", Address: netip.MustParseAddr("2001:db8::10"), Port: 443}

	manager.Ensure(t.Context(), endpoint)
	now = now.Add(9 * time.Minute)
	manager.Renew(t.Context())
	if len(client.requested) != 1 {
		t.Fatalf("requests before half lifetime = %d, want 1", len(client.requested))
	}

	now = now.Add(time.Minute)
	manager.Renew(t.Context())
	if len(client.requested) != 2 {
		t.Fatalf("requests at half lifetime = %d, want 2", len(client.requested))
	}
	if client.requested[0].Nonce != client.requested[1].Nonce {
		t.Fatal("renewal changed the mapping nonce")
	}

	manager.Remove(t.Context(), endpoint)
	if len(client.released) != 1 {
		t.Fatalf("releases = %d, want 1", len(client.released))
	}
	if len(manager.granted) != 0 {
		t.Fatalf("manager retained %d released mappings", len(manager.granted))
	}
}
