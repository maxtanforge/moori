package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/maxtanforge/moori/internal/control/storage"
	"github.com/maxtanforge/moori/internal/core/deployment"
	"github.com/maxtanforge/moori/internal/core/release"
	"github.com/maxtanforge/moori/internal/transport/agentwire"
)

func TestResultAckRequiresBoundAgentAndCommit(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.RegisterTarget(ctx, "project", "target"); err != nil {
		t.Fatal(err)
	}
	compiled, err := release.Compile(ctx, []byte("services: {web: {image: 'nginx@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'}}"), nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PublishRelease(ctx, "project", "release", compiled); err != nil {
		t.Fatal(err)
	}
	snapshot, err := compiled.Bind(release.Binding{Target: "target", ConfigVersion: "v1", HealthTimeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.AdmitSnapshot(ctx, storage.Admission{ID: "deploy", Project: "project", Caller: "ci", Key: "key", Target: "target", Release: "release"}, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := store.Claim(ctx, "deploy", "attempt", "agent", 2, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	service := Session{Store: store}
	peer := agentwire.Peer{ID: "agent", Generation: 2}
	old := agentwire.Peer{ID: "agent", Generation: 1}
	if _, err := service.Offer(ctx, old, "attempt", now); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("wrong offer peer: %v", err)
	}
	offer, err := service.Offer(ctx, peer, "attempt", now)
	if err != nil || offer.Kind != agentwire.KindOffer {
		t.Fatalf("offer: %+v %v", offer, err)
	}
	body, err := json.Marshal(agentwire.Report{ID: "event", Execution: "attempt", Sequence: 1, State: deployment.Executing})
	if err != nil {
		t.Fatal(err)
	}
	msg := agentwire.Message{Kind: agentwire.KindReport, Body: body}
	if _, err := service.Report(ctx, old, msg); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("wrong reporting peer: %v", err)
	}
	ack, err := service.Report(ctx, peer, msg)
	if err != nil || ack.Kind != agentwire.KindAck {
		t.Fatalf("ACK: %+v %v", ack, err)
	}
	view, err := store.Get(ctx, "deploy")
	if err != nil || view.State != deployment.Executing {
		t.Fatalf("uncommitted report: %+v %v", view, err)
	}
}
