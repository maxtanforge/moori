package maintenance

import (
	"context"
	"testing"
	"time"
)

func TestStopCancelsAndJoinsInFlightHousekeeping(t *testing.T) {
	entered, exited := make(chan struct{}), make(chan struct{})
	stop := Start(context.Background(), "test", time.Hour, func(ctx context.Context) error { close(entered); <-ctx.Done(); close(exited); return ctx.Err() })
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("startup housekeeping did not run")
	}
	stop()
	select {
	case <-exited:
	default:
		t.Fatal("stop returned before callback exited")
	}
}
