package ledger

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/maxtanforge/moori/internal/core/deployment"
)

func TestCancelAcceptedFencesDockerLaunch(t *testing.T) {
	ctx := context.Background()
	l, err := Open(ctx, filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if _, err := l.CancelAccepted(ctx, "unknown"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("unknown execution treated as canceled: %v", err)
	}
	now := time.Now()
	if accepted, err := l.Accept(ctx, Request{Execution: "held", Target: "target", Digest: "digest", Expires: now.Add(time.Minute)}, now); err != nil || !accepted {
		t.Fatalf("could not accept: %v %v", accepted, err)
	}
	for range 2 {
		if state, err := l.CancelAccepted(ctx, "held"); err != nil || state != deployment.Canceled {
			t.Fatalf("cancellation: %s %v", state, err)
		}
	}
	if err := l.Record(ctx, "held", deployment.Executing); !errors.Is(err, ErrState) {
		t.Fatalf("canceled deployment started: %v", err)
	}
	events, err := l.Pending(ctx, 10)
	if err != nil || len(events) != 1 || events[0].State != deployment.Canceled {
		t.Fatalf("duplicate cancellation events: %+v %v", events, err)
	}
}
