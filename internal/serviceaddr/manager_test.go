package serviceaddr

import (
	"bytes"
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"
)

type fakeAddressBackend struct {
	statuses map[netip.Addr][]AddressStatus
	ensured  []netip.Prefix
	removed  []netip.Prefix
}

func (f *fakeAddressBackend) Ensure(prefix netip.Prefix) error {
	f.ensured = append(f.ensured, prefix)
	return nil
}

func (f *fakeAddressBackend) Remove(prefix netip.Prefix) error {
	f.removed = append(f.removed, prefix)
	return nil
}

func (f *fakeAddressBackend) Status(address netip.Addr) (AddressStatus, error) {
	statuses := f.statuses[address]
	if len(statuses) == 0 {
		return AddressAbsent, nil
	}
	status := statuses[0]
	if len(statuses) > 1 {
		f.statuses[address] = statuses[1:]
	}
	return status, nil
}

func TestManagerEnsureWaitsForDAD(t *testing.T) {
	t.Parallel()

	deriver := testDeriver(t)
	prefix := netip.MustParsePrefix("2001:db8:1::/64")
	address, err := deriver.Address(prefix, "photos.example.com", 0)
	if err != nil {
		t.Fatal(err)
	}
	backend := &fakeAddressBackend{statuses: map[netip.Addr][]AddressStatus{
		address: {AddressTentative, AddressReady},
	}}
	manager, err := NewManager(backend, deriver)
	if err != nil {
		t.Fatal(err)
	}
	manager.pollInterval = time.Millisecond

	lease, err := manager.Ensure(t.Context(), prefix, "photos.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if lease.Prefix.Addr() != address || lease.DADCounter != 0 {
		t.Fatalf("lease = %+v", lease)
	}
	if len(backend.ensured) != 1 || backend.ensured[0] != lease.Prefix {
		t.Fatalf("ensured addresses = %v", backend.ensured)
	}
}

func TestManagerEnsureRetriesDADFailure(t *testing.T) {
	t.Parallel()

	deriver := testDeriver(t)
	prefix := netip.MustParsePrefix("2001:db8:1::/64")
	first, err := deriver.Address(prefix, "photos.example.com", 0)
	if err != nil {
		t.Fatal(err)
	}
	second, err := deriver.Address(prefix, "photos.example.com", 1)
	if err != nil {
		t.Fatal(err)
	}
	backend := &fakeAddressBackend{statuses: map[netip.Addr][]AddressStatus{
		first:  {AddressDADFailed},
		second: {AddressReady},
	}}
	manager, err := NewManager(backend, deriver)
	if err != nil {
		t.Fatal(err)
	}

	lease, err := manager.Ensure(t.Context(), prefix, "photos.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if lease.Prefix.Addr() != second || lease.DADCounter != 1 {
		t.Fatalf("lease = %+v", lease)
	}
	if len(backend.removed) != 1 || backend.removed[0].Addr() != first {
		t.Fatalf("removed addresses = %v", backend.removed)
	}
}

func TestManagerEnsureHonorsCancellation(t *testing.T) {
	t.Parallel()

	deriver := testDeriver(t)
	backend := &fakeAddressBackend{statuses: make(map[netip.Addr][]AddressStatus)}
	manager, err := NewManager(backend, deriver)
	if err != nil {
		t.Fatal(err)
	}
	manager.pollInterval = time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = manager.Ensure(ctx, netip.MustParsePrefix("2001:db8:1::/64"), "photos.example.com")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Ensure error = %v, want context cancellation", err)
	}
	if len(backend.ensured) != 0 {
		t.Fatalf("ensured addresses after cancellation = %v", backend.ensured)
	}
}

func TestManagerEnsureCleansUpAfterCancellation(t *testing.T) {
	t.Parallel()

	deriver := testDeriver(t)
	backend := &fakeAddressBackend{statuses: make(map[netip.Addr][]AddressStatus)}
	manager, err := NewManager(backend, deriver)
	if err != nil {
		t.Fatal(err)
	}
	manager.pollInterval = time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	_, err = manager.Ensure(ctx, netip.MustParsePrefix("2001:db8:1::/64"), "photos.example.com")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Ensure error = %v, want deadline exceeded", err)
	}
	if len(backend.ensured) != 1 || len(backend.removed) != 1 {
		t.Fatalf("ensured = %v, removed = %v", backend.ensured, backend.removed)
	}
}

func TestManagerRemove(t *testing.T) {
	t.Parallel()

	backend := &fakeAddressBackend{}
	manager, err := NewManager(backend, testDeriver(t))
	if err != nil {
		t.Fatal(err)
	}
	lease := Lease{Prefix: netip.MustParsePrefix("2001:db8:1::42/64")}

	if err := manager.Remove(lease); err != nil {
		t.Fatal(err)
	}
	if len(backend.removed) != 1 || backend.removed[0] != lease.Prefix {
		t.Fatalf("removed addresses = %v", backend.removed)
	}
}

func testDeriver(t *testing.T) Deriver {
	t.Helper()
	deriver, err := NewDeriver(bytes.Repeat([]byte{0x42}, minimumSecretSize))
	if err != nil {
		t.Fatal(err)
	}
	return deriver
}
