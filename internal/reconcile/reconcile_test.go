package reconcile

import (
	"context"
	"errors"
	"net/netip"
	"slices"
	"testing"
	"time"
)

func TestBuildPlan(t *testing.T) {
	t.Parallel()

	current := []Service{
		directService("removed", "2001:db8:1::10"),
		directService("updated", "2001:db8:1::20"),
		directService("unchanged", "2001:db8:1::30"),
	}
	desired := []Service{
		directService("created-b", "2001:db8:1::50"),
		directService("unchanged", "2001:db8:1::30"),
		directService("updated", "2001:db8:1::40"),
		directService("created-a", "2001:db8:1::60"),
	}

	plan, err := BuildPlan(desired, current)
	if err != nil {
		t.Fatal(err)
	}

	got := make([]string, 0, len(plan.Operations))
	for _, operation := range plan.Operations {
		got = append(got, string(operation.Kind)+":"+operation.ServiceID())
	}
	want := []string{"create:created-a", "create:created-b", "update:updated", "remove:removed"}
	if !slices.Equal(got, want) {
		t.Fatalf("operations = %v, want %v", got, want)
	}
}

func TestBuildPlanRejectsDuplicateService(t *testing.T) {
	t.Parallel()

	service := directService("photos", "2001:db8:1::10")
	if _, err := BuildPlan([]Service{service, service}, nil); err == nil {
		t.Fatal("BuildPlan accepted duplicate service IDs")
	}
}

type recordingExecutor struct {
	applied      []string
	verified     []string
	rolledBack   []string
	failApplyID  string
	failVerifyID string
}

func (e *recordingExecutor) Apply(_ context.Context, operation Operation) error {
	e.applied = append(e.applied, operation.ServiceID())
	if operation.ServiceID() == e.failApplyID {
		return errors.New("apply failed")
	}
	return nil
}

func (e *recordingExecutor) Verify(_ context.Context, operation Operation) error {
	e.verified = append(e.verified, operation.ServiceID())
	if operation.ServiceID() == e.failVerifyID {
		return errors.New("verify failed")
	}
	return nil
}

func (e *recordingExecutor) Rollback(_ context.Context, operation Operation) error {
	e.rolledBack = append(e.rolledBack, operation.ServiceID())
	return nil
}

func TestEngineExecute(t *testing.T) {
	t.Parallel()

	executor := &recordingExecutor{}
	engine, err := NewEngine(executor)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := BuildPlan([]Service{
		directService("photos", "2001:db8:1::10"),
		directService("media", "2001:db8:1::20"),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	if err := engine.Execute(t.Context(), plan, false); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(executor.applied, []string{"media", "photos"}) {
		t.Fatalf("applied = %v", executor.applied)
	}
	if !slices.Equal(executor.verified, executor.applied) {
		t.Fatalf("verified = %v, applied = %v", executor.verified, executor.applied)
	}
}

func TestEngineRollsBackInReverseOrder(t *testing.T) {
	t.Parallel()

	executor := &recordingExecutor{failVerifyID: "photos"}
	engine, err := NewEngine(executor)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := BuildPlan([]Service{
		directService("media", "2001:db8:1::10"),
		directService("photos", "2001:db8:1::20"),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	if err := engine.Execute(t.Context(), plan, false); err == nil {
		t.Fatal("Execute succeeded despite verification failure")
	}
	if !slices.Equal(executor.rolledBack, []string{"photos", "media"}) {
		t.Fatalf("rolled back = %v", executor.rolledBack)
	}
}

func TestEngineDryRunDoesNotExecute(t *testing.T) {
	t.Parallel()

	executor := &recordingExecutor{}
	engine, err := NewEngine(executor)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := BuildPlan([]Service{directService("photos", "2001:db8:1::10")}, nil)
	if err != nil {
		t.Fatal(err)
	}

	if err := engine.Execute(t.Context(), plan, true); err != nil {
		t.Fatal(err)
	}
	if len(executor.applied) != 0 {
		t.Fatalf("dry-run applied operations: %v", executor.applied)
	}
}

func TestRunSettledUsesLatestValue(t *testing.T) {
	t.Parallel()

	input := make(chan int, 3)
	input <- 1
	input <- 2
	input <- 3
	close(input)

	var handled []int
	err := RunSettled(t.Context(), time.Hour, input, func(_ context.Context, value int) error {
		handled = append(handled, value)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(handled, []int{3}) {
		t.Fatalf("handled = %v", handled)
	}
}

func directService(id, address string) Service {
	publicAddress := netip.MustParseAddr(address)
	return Service{
		ID:            id,
		DNSName:       id + ".example.com",
		Mode:          ModeDirect,
		PublicAddress: publicAddress,
		ListenPort:    443,
		Backend:       netip.AddrPortFrom(publicAddress, 443),
	}
}
