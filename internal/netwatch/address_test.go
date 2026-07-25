package netwatch

import (
	"net/netip"
	"strconv"
	"testing"
	"time"
)

func TestAddressStateCandidate(t *testing.T) {
	t.Parallel()

	observedAt := time.Date(2026, time.July, 25, 12, 0, 0, 0, time.UTC)
	state := addressState{
		prefix:            netip.MustParsePrefix("2001:db8:1::42/64"),
		preferredLifetime: 300,
		validLifetime:     600,
	}

	candidate, ok := state.candidate(observedAt)
	if !ok {
		t.Fatal("candidate was rejected")
	}
	if want := observedAt.Add(300 * time.Second); candidate.PreferredUntil != want {
		t.Fatalf("preferred deadline = %s, want %s", candidate.PreferredUntil, want)
	}
	if want := observedAt.Add(600 * time.Second); candidate.ValidUntil != want {
		t.Fatalf("valid deadline = %s, want %s", candidate.ValidUntil, want)
	}
	if candidate.Deprecated {
		t.Fatal("candidate is unexpectedly deprecated")
	}
}

func TestAddressStateCandidatePermanentLifetime(t *testing.T) {
	t.Parallel()

	state := addressState{
		prefix:            netip.MustParsePrefix("2001:db8:1::42/64"),
		preferredLifetime: infiniteLifetime,
		validLifetime:     infiniteLifetime,
	}

	candidate, ok := state.candidate(time.Now())
	if !ok {
		t.Fatal("candidate was rejected")
	}
	if !candidate.PreferredUntil.IsZero() || !candidate.ValidUntil.IsZero() {
		t.Fatalf("permanent candidate has finite deadlines: %+v", candidate)
	}
}

func TestAddressStateCandidateDeprecated(t *testing.T) {
	t.Parallel()

	state := addressState{
		prefix:        netip.MustParsePrefix("2001:db8:1::42/64"),
		validLifetime: 600,
	}

	candidate, ok := state.candidate(time.Now())
	if !ok {
		t.Fatal("candidate was rejected")
	}
	if !candidate.Deprecated {
		t.Fatal("zero preferred lifetime did not deprecate candidate")
	}
}

func TestAddressStateCandidateRejectsUnusableAddress(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		state addressState
	}{
		{name: "tentative", state: addressState{tentative: true}},
		{name: "DAD failed", state: addressState{dadFailed: true}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, ok := test.state.candidate(time.Now()); ok {
				t.Fatal("unusable address produced a candidate")
			}
		})
	}
}

func TestLifetimeSeconds(t *testing.T) {
	t.Parallel()

	type testCase struct {
		seconds int
		want    uint32
	}
	tests := []testCase{
		{seconds: -1, want: infiniteLifetime},
		{seconds: 300, want: 300},
	}
	if strconv.IntSize == 64 {
		maxLifetime := uint64(infiniteLifetime)
		tests = append(tests, testCase{seconds: int(maxLifetime), want: infiniteLifetime})
	}

	for _, test := range tests {
		if got := lifetimeSeconds(test.seconds); got != test.want {
			t.Fatalf("lifetimeSeconds(%d) = %d, want %d", test.seconds, got, test.want)
		}
	}
}
