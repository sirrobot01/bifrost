package reconcile

import (
	"context"
	"errors"
	"time"
)

// RunSettled passes the latest input value to handle after a quiet interval.
func RunSettled[T any](ctx context.Context, interval time.Duration, input <-chan T, handle func(context.Context, T) error) error {
	if interval <= 0 {
		return errors.New("settle interval must be positive")
	}
	if handle == nil {
		return errors.New("settle handler is required")
	}

	var latest T
	pending := false
	timer := time.NewTimer(interval)
	if !timer.Stop() {
		<-timer.C
	}
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case value, ok := <-input:
			if !ok {
				if pending {
					return handle(ctx, latest)
				}
				return nil
			}
			latest = value
			pending = true
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(interval)
		case <-timer.C:
			if !pending {
				continue
			}
			if err := handle(ctx, latest); err != nil {
				return err
			}
			pending = false
		}
	}
}
