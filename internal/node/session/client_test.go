package session_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maxtanforge/moori/internal/core/deployment"
	"github.com/maxtanforge/moori/internal/core/nodeops"
	"github.com/maxtanforge/moori/internal/core/release"
	"github.com/maxtanforge/moori/internal/node/ledger"
	"github.com/maxtanforge/moori/internal/node/session"
	"github.com/maxtanforge/moori/internal/transport/agentwire"
)

func TestClientResumesOnlyNeverStartedWorkAfterReopen(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	path := filepath.Join(t.TempDir(), "agent.db")
	l, err := ledger.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := release.Compile(ctx, []byte("services: {web: {image: 'nginx@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'}}"), nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for _, id := range []string{"already-started", "never-started"} {
		snapshot, err := compiled.Bind(release.Binding{Target: id, ConfigVersion: "v1", HealthTimeout: time.Minute})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := l.Accept(ctx, ledger.Request{Execution: id, Target: id, Digest: snapshot.Digest(), Document: snapshot.JSON(), Expires: now.Add(time.Minute)}, now); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.Record(ctx, "already-started", deployment.Executing); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	l, err = ledger.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	launched := make(chan string, 2)
	done := make(chan error, 1)
	client := session.Client{
		Session: session.Session{Ledger: l},
		Dial:    func(context.Context) (*agentwire.Channel, error) { return nil, errors.New("offline") },
		Execute: func(ctx context.Context, id, document string) error {
			if err := l.Record(ctx, id, deployment.Executing); err != nil {
				return err
			}
			launched <- id
			return nil
		},
	}
	go func() { done <- client.Run(ctx) }()
	defer func() { cancel(); <-done }()
	select {
	case id := <-launched:
		if id != "never-started" {
			t.Fatalf("restarted uncertain execution: %s", id)
		}
	case <-ctx.Done():
		t.Fatal("durably accepted execution did not resume")
	}
	entry, err := l.Get(ctx, "already-started")
	if err != nil || entry.State != deployment.Reconciling || entry.Sequence != 2 {
		t.Fatalf("interrupted execution was not recorded for reconciliation: %+v %v", entry, err)
	}
	events, err := l.Pending(ctx, 10)
	if err != nil || len(events) < 2 || events[1].State != deployment.Reconciling || events[1].Execution != "already-started" {
		t.Fatalf("missing ordered interruption report: %+v %v", events, err)
	}
}

func TestClientReportsCompletedLogWithoutWaitingForPoll(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	book, err := ledger.Open(ctx, filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer book.Close()

	const id = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	req := nodeops.Request{Action: nodeops.ContainerLogs, Target: strings.Repeat("a", 64), Tail: 200, Generation: 1}
	reading := make(chan struct{})
	reported := make(chan agentwire.NodeOperationResult, 1)
	var offered bool
	client := session.Client{
		Session:  session.Session{Ledger: book},
		Interval: 5 * time.Second,
		Execute: func(context.Context, string, string) error {
			return errors.New("unexpected deployment")
		},
		NodeOperation: func(_ context.Context, received nodeops.Request) (nodeops.Result, error) {
			close(reading)
			if received.Action != req.Action || received.Target != req.Target || received.Tail != req.Tail {
				return nodeops.Result{}, errors.New("wrong log request")
			}
			return nodeops.Result{Log: "container output"}, nil
		},
		Direct: func(_ context.Context, m agentwire.Message) (agentwire.Message, error) {
			switch m.Kind {
			case agentwire.KindNodeOperationPoll:
				if !offered {
					offered = true
					return agentwire.Encode(agentwire.KindNodeOperation, agentwire.NodeOperation{
						ID: id, Request: req, ExpiresMS: time.Now().Add(req.Action.Timeout() + 15*time.Second).UnixMilli(),
					})
				}
			case agentwire.KindNodeOperationResult:
				var result agentwire.NodeOperationResult
				if err := agentwire.Decode(m, m.Kind, &result); err != nil {
					return agentwire.Message{}, err
				}
				reported <- result
				return agentwire.Encode(agentwire.KindNodeOperationResultAck, agentwire.Ack{ID: result.ID})
			}
			return agentwire.Encode(agentwire.KindIdle, struct{}{})
		},
	}
	done := make(chan error, 1)
	go func() { done <- client.Run(ctx) }()
	defer func() { cancel(); <-done }()
	select {
	case <-reading:
	case <-ctx.Done():
		t.Fatal("client never started reading logs")
	}
	select {
	case result := <-reported:
		if result.ID != id || result.State != nodeops.Succeeded || result.Result == nil || result.Result.Log != "container output" {
			t.Fatalf("unexpected result: %+v", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("completed log operation did not wake the session before the next 5-second poll")
	}
}
