package home

import (
	"context"
	"errors"
	"net/netip"
	"sync"
	"testing"

	"github.com/sirrobot01/bifrost/internal/diagnose"
	"github.com/sirrobot01/bifrost/internal/notify"
)

type scriptedProber struct {
	mu       sync.Mutex
	results  []diagnose.ProbeResult
	failures []error
	calls    int
}

func (p *scriptedProber) Probe(context.Context, diagnose.ProbeRequest) (diagnose.ProbeResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	index := min(p.calls, len(p.results)-1)
	p.calls++
	if index < len(p.failures) && p.failures[index] != nil {
		return diagnose.ProbeResult{}, p.failures[index]
	}
	return p.results[index], nil
}

type recordingNotifier struct {
	mu     sync.Mutex
	events []notify.Event
}

func (n *recordingNotifier) Notify(event notify.Event) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.events = append(n.events, event)
}

func (n *recordingNotifier) kinds() []notify.Kind {
	n.mu.Lock()
	defer n.mu.Unlock()
	kinds := make([]notify.Kind, 0, len(n.events))
	for _, event := range n.events {
		kinds = append(kinds, event.Kind)
	}
	return kinds
}

func target() verifyTarget {
	return verifyTarget{Service: "media", DNSName: "media.example.com", Address: netip.MustParseAddr("2a01:db8:1::10"), Port: 443}
}

// A steady service must produce one notification when it breaks and one when it
// recovers, not one per probe.
func TestVerifierNotifiesOnlyOnTransitions(t *testing.T) {
	t.Parallel()

	prober := &scriptedProber{results: []diagnose.ProbeResult{
		{Reachable: true},
		{Reachable: true},
		{Reachable: false},
		{Reachable: false},
		{Reachable: true},
	}}
	notifier := &recordingNotifier{}
	verifier := newVerifier(prober, notifier, nil)

	for range 5 {
		verifier.Verify(t.Context(), []verifyTarget{target()})
	}

	kinds := notifier.kinds()
	if len(kinds) != 2 || kinds[0] != notify.ExternalLost || kinds[1] != notify.ExternalRestored {
		t.Fatalf("kinds = %v, want one lost then one restored", kinds)
	}
}

// The first successful probe after startup is the expected state, not news.
func TestVerifierIsSilentWhenHealthyFromTheStart(t *testing.T) {
	t.Parallel()

	prober := &scriptedProber{results: []diagnose.ProbeResult{{Reachable: true}}}
	notifier := &recordingNotifier{}
	verifier := newVerifier(prober, notifier, nil)

	verifier.Verify(t.Context(), []verifyTarget{target()})
	if kinds := notifier.kinds(); len(kinds) != 0 {
		t.Fatalf("kinds = %v, want none", kinds)
	}
	states := verifier.States()
	if len(states) != 1 || !states[0].Reachable {
		t.Fatalf("states = %+v", states)
	}
}

// A probe that cannot run says so. Treating it as a healthy service would
// recreate the failure this whole mechanism exists to catch, and treating it as
// a silent failure would hide a broken edge.
func TestVerifierReportsAProbeFailureWithItsReason(t *testing.T) {
	t.Parallel()

	prober := &scriptedProber{
		results:  []diagnose.ProbeResult{{}},
		failures: []error{errors.New("edge dial timed out")},
	}
	notifier := &recordingNotifier{}
	verifier := newVerifier(prober, notifier, nil)

	verifier.Verify(t.Context(), []verifyTarget{target()})

	states := verifier.States()
	if len(states) != 1 || states[0].Reachable {
		t.Fatalf("states = %+v", states)
	}
	if want := "the probe could not complete: edge dial timed out"; states[0].Detail != want {
		t.Fatalf("detail = %q, want %q", states[0].Detail, want)
	}
	if kinds := notifier.kinds(); len(kinds) != 1 || kinds[0] != notify.ExternalLost {
		t.Fatalf("kinds = %v", kinds)
	}
}

// Without a prober there is no evidence, so there must be no verdict to export.
func TestVerifierExportsNothingWithoutAProber(t *testing.T) {
	t.Parallel()

	verifier := newVerifier(nil, nil, nil)
	verifier.Verify(t.Context(), []verifyTarget{target()})
	if states := verifier.States(); states != nil {
		t.Fatalf("states = %+v, want none", states)
	}
}

// A withdrawn service must stop being reported, or its last verdict becomes a
// permanent stale metric.
func TestVerifierForgetsServicesThatAreNoLongerPublished(t *testing.T) {
	t.Parallel()

	prober := &scriptedProber{results: []diagnose.ProbeResult{{Reachable: true}}}
	verifier := newVerifier(prober, nil, nil)

	verifier.Verify(t.Context(), []verifyTarget{target(), {Service: "photos", DNSName: "photos.example.com", Address: netip.MustParseAddr("2a01:db8:1::11"), Port: 443}})
	if states := verifier.States(); len(states) != 2 {
		t.Fatalf("states = %+v, want 2", states)
	}
	verifier.Verify(t.Context(), []verifyTarget{target()})
	states := verifier.States()
	if len(states) != 1 || states[0].Service != "media" {
		t.Fatalf("states = %+v, want only media", states)
	}
}
