package netwatch

import "context"

// Observer reports the current IPv6 state of an interface and emits a fresh
// snapshot whenever that state changes. Implementations use the native event
// source where the operating system exposes one and a bounded poll elsewhere.
type Observer interface {
	Snapshot() (Snapshot, error)
	Observe(context.Context, chan<- Snapshot) error
}
