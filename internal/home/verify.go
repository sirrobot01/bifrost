package home

import (
	"context"
	"log/slog"
	"net/netip"
	"sync"
	"time"

	"github.com/sirrobot01/bifrost/internal/diagnose"
	"github.com/sirrobot01/bifrost/internal/notify"
)

// VerificationState is what the daemon currently knows about one service's
// reachability from outside the network.
type VerificationState struct {
	Service   string
	DNSName   string
	Reachable bool
	Detail    string
	CheckedAt time.Time
}

// verifier answers, repeatedly, the one question no local check can: does this
// service answer from outside?
//
// `check` already asks it, but `check` is a command an operator runs while
// setting a host up. Every guarantee it gives expires when they close the
// terminal. The outage that motivated this work would recur unchanged if its
// cause arrived a week later: the reconcile loop would keep succeeding, the
// readiness gauge would stay at 1, and nothing in the process would hold a
// contrary opinion until a user complained.
type verifier struct {
	prober   diagnose.ExternalProber
	notifier notify.Notifier
	logger   *slog.Logger
	timeout  time.Duration
	now      func() time.Time

	mu     sync.RWMutex
	states map[string]VerificationState
}

type verifyTarget struct {
	Service string
	DNSName string
	Address netip.Addr
	Port    uint16
}

func newVerifier(prober diagnose.ExternalProber, notifier notify.Notifier, logger *slog.Logger) *verifier {
	if notifier == nil {
		notifier = notify.Discard{}
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &verifier{
		prober:   prober,
		notifier: notifier,
		logger:   logger,
		timeout:  30 * time.Second,
		now:      time.Now,
		states:   make(map[string]VerificationState),
	}
}

// Verify probes every target once and records the outcome. Only transitions are
// logged and notified: a service that has been reachable for a month should
// produce no output at all.
func (v *verifier) Verify(ctx context.Context, targets []verifyTarget) {
	if v == nil || v.prober == nil {
		return
	}
	seen := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		seen[target.Service] = struct{}{}
		v.verifyOne(ctx, target)
		if ctx.Err() != nil {
			return
		}
	}
	// A service that is no longer published has no reachability to report, and
	// leaving its last verdict behind would keep exporting a stale metric.
	v.mu.Lock()
	for name := range v.states {
		if _, published := seen[name]; !published {
			delete(v.states, name)
		}
	}
	v.mu.Unlock()
}

func (v *verifier) verifyOne(ctx context.Context, target verifyTarget) {
	probeContext, cancel := context.WithTimeout(ctx, v.timeout)
	defer cancel()

	result, err := v.prober.Probe(probeContext, diagnose.ProbeRequest{
		Address:    target.Address,
		Port:       target.Port,
		ServerName: target.DNSName,
	})
	// A probe that could not run is not evidence that the service is down, so
	// it is recorded as unreachable-with-a-reason rather than silently as a
	// failure of the service itself. The distinction is in the detail line,
	// which is what an operator reads.
	state := VerificationState{
		Service:   target.Service,
		DNSName:   target.DNSName,
		Reachable: err == nil && result.Reachable,
		CheckedAt: v.now(),
	}
	switch {
	case err != nil:
		state.Detail = "the probe could not complete: " + err.Error()
	case !result.Reachable:
		state.Detail = result.Detail
		if state.Detail == "" {
			state.Detail = "the service did not answer from outside this network"
		}
	}

	v.mu.Lock()
	previous, known := v.states[target.Service]
	v.states[target.Service] = state
	v.mu.Unlock()

	if known && previous.Reachable == state.Reachable {
		return
	}
	switch {
	case !state.Reachable:
		v.logger.Warn("service is not reachable from outside this network", "service", target.Service, "dns", target.DNSName, "detail", state.Detail)
		v.notifier.Notify(notify.Event{
			Kind:    notify.ExternalLost,
			Service: target.Service,
			Summary: "not reachable from outside this network",
			Detail:  state.Detail,
			At:      state.CheckedAt,
		})
	case known:
		// Only announce recovery from a known outage. A first successful probe
		// after startup is the expected state, not news.
		v.logger.Info("service is reachable from outside this network again", "service", target.Service, "dns", target.DNSName)
		v.notifier.Notify(notify.Event{
			Kind:    notify.ExternalRestored,
			Service: target.Service,
			Summary: "reachable from outside this network again",
			At:      state.CheckedAt,
		})
	default:
		v.logger.Info("service is reachable from outside this network", "service", target.Service, "dns", target.DNSName)
	}
}

// States returns the current verdicts, or nothing when no prober is configured.
// Absent is deliberate: a deployment that cannot look from outside must not
// export an optimistic metric.
func (v *verifier) States() []VerificationState {
	if v == nil || v.prober == nil {
		return nil
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	states := make([]VerificationState, 0, len(v.states))
	for _, state := range v.states {
		states = append(states, state)
	}
	return states
}
