package ledger

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/maxtanforge/moori/internal/core/deployment"
)

func TestRestartDoesNotGrantDuplicateExecution(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "agent.db")
	now := time.Now().UTC().Truncate(time.Second)
	request := Request{Execution: "attempt", Target: "target", Digest: "digest", Expires: now.Add(time.Hour)}
	l, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := l.Accept(ctx, request, now)
	if err != nil || !accepted {
		t.Fatalf("first acceptance: %v %v", accepted, err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	l, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	accepted, err = l.Accept(ctx, request, now)
	if err != nil || accepted {
		t.Fatalf("duplicate acceptance: %v %v", accepted, err)
	}
	changed := request
	changed.Digest = "changed"
	if _, err := l.Accept(ctx, changed, now); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed input: %v", err)
	}
	other := request
	other.Execution = "other"
	if _, err := l.Accept(ctx, other, now); !errors.Is(err, ErrBusy) {
		t.Fatalf("other execution: %v", err)
	}
	expired := Request{Execution: "expired", Target: "independent", Digest: "digest", Expires: now}
	if _, err := l.Accept(ctx, expired, now); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired input: %v", err)
	}
}

func TestResultsSurviveRestartUntilAcknowledged(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "agent.db")
	now := time.Now().UTC().Truncate(time.Second)
	request := Request{Execution: "attempt", Target: "target", Digest: "digest", Expires: now.Add(time.Hour)}
	l, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Accept(ctx, request, now); err != nil {
		t.Fatal(err)
	}
	for _, state := range []deployment.State{deployment.Executing, deployment.Verifying, deployment.Succeeded} {
		if err := l.Record(ctx, request.Execution, state); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	l, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	events, err := l.Pending(ctx, 100)
	if err != nil || len(events) != 3 || events[2].State != deployment.Succeeded || events[2].Sequence != 3 {
		t.Fatalf("events: %+v %v", events, err)
	}
	if err := l.Acknowledge(ctx, events[2].ID); err != nil {
		t.Fatal(err)
	}
	if err := l.Prune(ctx, request.Expires); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Get(ctx, request.Execution); err != nil {
		t.Fatalf("pruned before all acknowledgements: %v", err)
	}
	for _, e := range events[:2] {
		if err := l.Acknowledge(ctx, e.ID); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.Prune(ctx, request.Expires); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Accept(ctx, request, now); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired replay after clock moved backward: %v", err)
	}
}
