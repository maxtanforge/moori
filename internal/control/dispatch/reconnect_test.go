package dispatch_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maxtanforge/moori/internal/control/dispatch"
	"github.com/maxtanforge/moori/internal/control/storage"
	"github.com/maxtanforge/moori/internal/core/deployment"
	"github.com/maxtanforge/moori/internal/core/release"
	"github.com/maxtanforge/moori/internal/node/executor"
	"github.com/maxtanforge/moori/internal/node/ledger"
	"github.com/maxtanforge/moori/internal/node/session"
	"github.com/maxtanforge/moori/internal/transport/agentwire"
)

func TestReconnectReplaysResultWithoutExecutingAgain(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	dir := t.TempDir()
	control, err := storage.Open(ctx, filepath.Join(dir, "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	node, err := ledger.Open(ctx, filepath.Join(dir, "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer node.Close()
	if err := control.RegisterTarget(ctx, "project", "target"); err != nil {
		t.Fatal(err)
	}
	if err := control.AssignTarget(ctx, "target", "agent", 1); err != nil {
		t.Fatal(err)
	}
	r, err := release.Compile(ctx, []byte("services: {web: {image: 'nginx@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'}}"), nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := control.PublishRelease(ctx, "project", "release", r); err != nil {
		t.Fatal(err)
	}
	snapshot, err := r.Bind(release.Binding{Target: "target", ConfigVersion: "v1", HealthTimeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	id, err := control.AdmitSnapshot(ctx, storage.Admission{ID: "deploy", Project: "project", Caller: "ci", Key: "deploy", Target: "target", Release: "release"}, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	agentToken, _, err := control.IssueAgentCredential(ctx, "agent", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	service := dispatch.Session{Store: control}
	var lost atomic.Bool
	var connections atomic.Int32
	srv := httptest.NewUnstartedServer(agentwire.Handler{
		Authenticate: dispatch.AuthenticateCredential(control),
		Serve: func(ctx context.Context, peer agentwire.Peer, ch *agentwire.Channel) {
			connections.Add(1)
			for {
				m, err := ch.Receive()
				if err != nil {
					return
				}
				reply, err := service.Reply(ctx, peer, m)
				if err != nil {
					return
				}
				var result agentwire.Report
				if m.Kind == agentwire.KindReport && agentwire.Decode(m, agentwire.KindReport, &result) == nil &&
					result.State == deployment.Succeeded && lost.CompareAndSwap(false, true) {
					return // transaction committed, but the Agent did not receive its ACK
				}
				if err := ch.Send(reply); err != nil {
					return
				}
			}
		},
	})
	srv.StartTLS()
	defer srv.Close()
	serverRoots := x509.NewCertPool()
	serverRoots.AddCert(srv.Certificate())
	var runs atomic.Int32
	runner := executor.Runner{Ledger: node, Apply: func(context.Context, executor.Plan) error { runs.Add(1); return nil }}
	client := session.Client{Session: session.Session{Ledger: node}, Execute: runner.RunAccepted, Interval: 20 * time.Millisecond,
		Dial: func(ctx context.Context) (*agentwire.Channel, error) {
			return agentwire.DialCredential(ctx, "wss"+strings.TrimPrefix(srv.URL, "https"), &tls.Config{RootCAs: serverRoots}, agentToken)
		},
	}
	done := make(chan error, 1)
	go func() { done <- client.Run(ctx) }()
	defer func() { cancel(); <-done }()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			t.Fatalf("no successful reconnect: connections=%d executions=%d lost=%v", connections.Load(), runs.Load(), lost.Load())
		case <-tick.C:
			if !lost.Load() || connections.Load() < 2 {
				continue
			}
			pending, err := node.Pending(ctx, 10)
			if err != nil {
				t.Fatal(err)
			}
			if len(pending) != 0 {
				continue
			}
			view, err := control.Get(ctx, id)
			if err != nil || view.State != deployment.Succeeded || view.LastSuccess != "release" || runs.Load() != 1 {
				t.Fatalf("unexpected replay result: %+v, executions=%d, err=%v", view, runs.Load(), err)
			}
			return
		}
	}
}
