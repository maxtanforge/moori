// Package maintenance runs bounded periodic housekeeping with an owned lifetime.
package maintenance

import (
	"context"
	"log/slog"
	"time"
)

func Start(parent context.Context, name string, interval time.Duration, run func(context.Context) error) func() {
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			if err := run(ctx); err != nil && ctx.Err() == nil {
				slog.Warn(name, "err", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return func() { cancel(); <-done }
}
