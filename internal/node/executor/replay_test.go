package executor

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/maxtanforge/moori/internal/control/dispatch"
	"github.com/maxtanforge/moori/internal/control/storage"
	"github.com/maxtanforge/moori/internal/core/deployment"
	"github.com/maxtanforge/moori/internal/core/release"
	"github.com/maxtanforge/moori/internal/node/ledger"
	"github.com/maxtanforge/moori/internal/node/session"
	"github.com/maxtanforge/moori/internal/transport/agentwire"
)

func TestAgentReplaysCommittedResultAfterLostAcknowledgement(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	control, err := storage.Open(ctx, filepath.Join(root, "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	node, err := ledger.Open(ctx, filepath.Join(root, "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer node.Close()

	r, err := release.Compile(ctx, []byte("services: {web: {image: 'nginx@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'}}"), nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := r.Bind(release.Binding{Target: "target", ConfigVersion: "v1", HealthTimeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if err := control.RegisterTarget(ctx, "project", "target"); err != nil {
		t.Fatal(err)
	}
	if err := control.PublishRelease(ctx, "project", "release", r); err != nil {
		t.Fatal(err)
	}
	request := storage.Admission{ID: "deploy", Project: "project", Caller: "ci", Key: "request", Target: "target", Release: "release"}
	id, err := control.AdmitSnapshot(ctx, request, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := control.Claim(ctx, id, "attempt", "agent", 2, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	controller := dispatch.Session{Store: control}
	agent := session.Session{Ledger: node}
	msg, err := controller.Offer(ctx, agentwire.Peer{ID: "agent", Generation: 2}, "attempt", now)
	if err != nil {
		t.Fatal(err)
	}
	frame, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	var offer agentwire.Message
	if err := json.Unmarshal(frame, &offer); err != nil {
		t.Fatal(err)
	}
	accepted, err := agent.Accept(ctx, offer, now)
	if err != nil || !accepted.Start {
		t.Fatalf("agent acceptance: %+v %v", accepted, err)
	}
	duplicate, err := agent.Accept(ctx, offer, now)
	if err != nil || duplicate.Start {
		t.Fatalf("duplicate acceptance: %+v %v", duplicate, err)
	}
	if err := control.MarkReconciling(ctx, "attempt", 2); err != nil {
		t.Fatal(err)
	}
	runner := Runner{Ledger: node, Apply: func(context.Context, Plan) error { return nil }}
	if err := runner.RunAccepted(ctx, accepted.Execution, accepted.Document); err != nil {
		t.Fatal(err)
	}
	pending, err := node.Pending(ctx, 10)
	if err != nil || len(pending) != 3 {
		t.Fatalf("events: %+v %v", pending, err)
	}

	report := func(e ledger.Event, generation int64) (agentwire.Message, error) {
		frame, err := session.Report(e)
		if err != nil {
			return agentwire.Message{}, err
		}
		return controller.Report(ctx, agentwire.Peer{ID: "agent", Generation: generation}, frame)
	}
	if _, err := report(pending[0], 1); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("old identity accepted: %v", err)
	}
	if _, err := report(pending[2], 2); !errors.Is(err, storage.ErrState) {
		t.Fatalf("out-of-order result accepted: %v", err)
	}
	for _, event := range pending {
		if _, err := report(event, 2); err != nil {
			t.Fatal(err)
		}
	}
	// The first delivery committed at the control plane, but every ACK was lost.
	pending, err = node.Pending(ctx, 10)
	if err != nil || len(pending) != 3 {
		t.Fatalf("events were lost: %+v %v", pending, err)
	}
	for _, event := range pending {
		ack, err := report(event, 2)
		if err != nil {
			t.Fatal(err)
		}
		if err := agent.Acknowledge(ctx, event, ack); err != nil {
			t.Fatal(err)
		}
	}
	pending, err = node.Pending(ctx, 10)
	if err != nil || len(pending) != 0 {
		t.Fatalf("events were not acknowledged: %+v %v", pending, err)
	}
	view, err := control.Get(ctx, id)
	if err != nil || view.State != deployment.Succeeded || view.LastSuccess != "release" {
		t.Fatalf("control result: %+v %v", view, err)
	}
}
