package home

import "time"

// reconcileBackoff doubles the settle window per consecutive failure, capped
// at ten minutes. A permanently failing provider is retried a few times per
// hour instead of several times per minute, while a brief outage still
// recovers within a couple of windows.
func reconcileBackoff(settle time.Duration, failures int) time.Duration {
	const ceiling = 10 * time.Minute
	delay := settle
	for i := 1; i < failures && delay < ceiling; i++ {
		delay *= 2
	}
	return min(delay, ceiling)
}
