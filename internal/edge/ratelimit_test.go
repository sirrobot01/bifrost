package edge

import (
	"math/rand"
	"net/netip"
	"testing"
	"time"
)

func sourceAt(index int) netip.Addr {
	return netip.AddrFrom4([4]byte{byte(index >> 24), byte(index >> 16), byte(index >> 8), byte(index)})
}

func TestSourceRateLimiterEnforcesBurstThenRefills(t *testing.T) {
	t.Parallel()

	limiter := newSourceRateLimiter(120, 20)
	source := netip.MustParseAddr("192.0.2.10")
	for attempt := range 20 {
		if !limiter.Allow(source) {
			t.Fatalf("burst attempt %d denied", attempt)
		}
	}
	if limiter.Allow(source) {
		t.Fatal("expected the source to be limited once its burst is spent")
	}

	// Age the bucket by half a token at 120/minute, which is not yet enough.
	limiter.buckets[source].last = time.Now().Add(-250 * time.Millisecond)
	if limiter.Allow(source) {
		t.Fatal("expected the source to stay limited before a whole token refills")
	}
	limiter.buckets[source].last = time.Now().Add(-time.Second)
	if !limiter.Allow(source) {
		t.Fatal("expected the source to be allowed after a token refilled")
	}
}

// A flood wide enough to fill the bucket table must not lock out clients that
// are not already in it.
func TestSourceRateLimiterAdmitsNewSourcesWhenTableIsFull(t *testing.T) {
	t.Parallel()

	limiter := newSourceRateLimiter(120, 20)
	for index := range maxSourceBuckets {
		if !limiter.Allow(sourceAt(index)) {
			t.Fatalf("flood source %d denied", index)
		}
	}
	if len(limiter.buckets) != maxSourceBuckets {
		t.Fatalf("buckets = %d, want %d", len(limiter.buckets), maxSourceBuckets)
	}
	victim := netip.MustParseAddr("203.0.113.9")
	if !limiter.Allow(victim) {
		t.Fatal("a new source was denied because the table was full")
	}
	if len(limiter.buckets) > maxSourceBuckets {
		t.Fatalf("buckets = %d, want the table to stay bounded at %d", len(limiter.buckets), maxSourceBuckets)
	}
}

// Reclaiming space for new sources must not hand a throttled source a fresh
// bucket, which would make the limit trivially bypassable under table pressure.
func TestSourceRateLimiterDoesNotResetAThrottledSource(t *testing.T) {
	t.Parallel()

	limiter := newSourceRateLimiter(120, 20)
	attacker := netip.MustParseAddr("192.0.2.66")
	for range 20 {
		limiter.Allow(attacker)
	}
	if limiter.Allow(attacker) {
		t.Fatal("expected the attacker to be limited")
	}
	// Fill the table so every admission has to reclaim an existing bucket.
	for index := range maxSourceBuckets {
		limiter.Allow(sourceAt(index))
	}
	if limiter.Allow(attacker) {
		t.Fatal("a throttled source regained its burst through table pressure")
	}
}

// Idle buckets must be reclaimed so the table drains after a flood ends.
func TestSourceRateLimiterReclaimsIdleBuckets(t *testing.T) {
	t.Parallel()

	limiter := newSourceRateLimiter(120, 20)
	const flood = 1000
	for index := range flood {
		limiter.Allow(sourceAt(index))
	}
	if len(limiter.buckets) != flood {
		t.Fatalf("buckets = %d, want %d", len(limiter.buckets), flood)
	}
	// Every bucket has now been idle long enough to have refilled completely.
	stale := time.Now().Add(-2 * limiter.idle)
	for _, bucket := range limiter.buckets {
		bucket.last = stale
	}
	probe := netip.MustParseAddr("198.51.100.1")
	for range flood/maxBucketEvictions + 2 {
		limiter.Allow(probe)
	}
	if len(limiter.buckets) > maxBucketEvictions+1 {
		t.Fatalf("buckets = %d, want the idle entries reclaimed", len(limiter.buckets))
	}
}

// Expiry must not scan the table, so the cost of one decision has to stay flat
// as the table grows.
func TestSourceRateLimiterCostDoesNotGrowWithTableSize(t *testing.T) {
	t.Parallel()

	measure := func(fill int) time.Duration {
		limiter := newSourceRateLimiter(120, 20)
		for index := range fill {
			limiter.Allow(sourceAt(index))
		}
		probe := netip.MustParseAddr("198.51.100.7")
		limiter.Allow(probe)
		const repetitions = 2000
		start := time.Now()
		for range repetitions {
			limiter.Allow(probe)
		}
		return time.Since(start) / repetitions
	}

	small := measure(1000)
	large := measure(40000)
	t.Logf("per Allow: 1k buckets = %v, 40k buckets = %v", small, large)
	// The linear sweep this replaced was ~20x slower at 40k than at 1k. Allow a
	// wide margin for scheduling noise while still catching a reintroduced scan.
	if large > 8*small+time.Microsecond {
		t.Fatalf("cost scaled with table size: %v at 1k buckets, %v at 40k", small, large)
	}
}

func checkListInvariants(t *testing.T, limiter *sourceRateLimiter, label string) {
	t.Helper()

	seen := make(map[netip.Addr]bool, len(limiter.buckets))
	forward := 0
	for bucket := limiter.newest; bucket != nil; bucket = bucket.older {
		if seen[bucket.source] {
			t.Fatalf("%s: list cycles at %v", label, bucket.source)
		}
		seen[bucket.source] = true
		forward++
		if bucket.older != nil && bucket.older.newer != bucket {
			t.Fatalf("%s: broken link at %v", label, bucket.source)
		}
		if bucket.older != nil && bucket.older.last.After(bucket.last) {
			t.Fatalf("%s: list is not ordered by last use at %v", label, bucket.source)
		}
	}
	if forward != len(limiter.buckets) {
		t.Fatalf("%s: list holds %d buckets, map holds %d", label, forward, len(limiter.buckets))
	}
	backward := 0
	for bucket := limiter.oldest; bucket != nil; bucket = bucket.newer {
		backward++
	}
	if backward != forward {
		t.Fatalf("%s: reverse walk saw %d buckets, forward walk saw %d", label, backward, forward)
	}
	for source := range limiter.buckets {
		if !seen[source] {
			t.Fatalf("%s: %v is in the map but not the list", label, source)
		}
	}
	if limiter.newest != nil && limiter.newest.newer != nil {
		t.Fatalf("%s: newest bucket has a newer neighbour", label)
	}
	if limiter.oldest != nil && limiter.oldest.older != nil {
		t.Fatalf("%s: oldest bucket has an older neighbour", label)
	}
	if len(limiter.buckets) > maxSourceBuckets {
		t.Fatalf("%s: table holds %d buckets, over the %d cap", label, len(limiter.buckets), maxSourceBuckets)
	}
}

// The list and the map are updated by hand on every path, so exercise reuse,
// full-table reclaim and idle eviction and assert they never drift apart.
func TestSourceRateLimiterListInvariants(t *testing.T) {
	t.Parallel()

	random := rand.New(rand.NewSource(7))

	t.Run("reuse", func(t *testing.T) {
		limiter := newSourceRateLimiter(120, 20)
		for step := range 100000 {
			limiter.Allow(sourceAt(random.Intn(300)))
			if step%10000 == 0 {
				checkListInvariants(t, limiter, "reuse")
			}
		}
		checkListInvariants(t, limiter, "reuse")
	})

	t.Run("table pressure", func(t *testing.T) {
		limiter := newSourceRateLimiter(120, 20)
		for step := range 200000 {
			limiter.Allow(sourceAt(random.Intn(100000)))
			if step%25000 == 0 {
				checkListInvariants(t, limiter, "pressure")
			}
		}
		checkListInvariants(t, limiter, "pressure")
		if len(limiter.buckets) != maxSourceBuckets {
			t.Fatalf("table = %d, want it held at the %d cap", len(limiter.buckets), maxSourceBuckets)
		}
	})

	t.Run("idle eviction", func(t *testing.T) {
		limiter := newSourceRateLimiter(120, 20)
		age := func() {
			stale := time.Now().Add(-3 * limiter.idle)
			for _, bucket := range limiter.buckets {
				bucket.last = stale
			}
		}
		for round := range 200 {
			for range 50 {
				limiter.Allow(sourceAt(random.Intn(500)))
			}
			if round%2 == 0 {
				age()
			}
			checkListInvariants(t, limiter, "idle")
		}
		age()
		probe := netip.MustParseAddr("198.51.100.2")
		for range 500 {
			limiter.Allow(probe)
		}
		checkListInvariants(t, limiter, "idle")
		if len(limiter.buckets) != 1 {
			t.Fatalf("table = %d, want it drained to the single live source", len(limiter.buckets))
		}
	})
}
