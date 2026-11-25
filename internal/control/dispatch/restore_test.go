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

func TestRestoredControlReceivesInventoryAndDispatchesOnlyAfterRelease(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	dir := t.TempDir()
	live, backup, copy := filepath.Join(dir, "control.db"), filepath.Join(dir, "backup.db"), filepath.Join(dir, "recovery.db")
	control, err := storage.Open(ctx, live)
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	agentToken, _, err := control.IssueAgentCredential(ctx, "agent", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := control.RegisterAssignedTarget(ctx, "project", "target", "agent", 1); err != nil {
		t.Fatal(err)
	}
	if err := storage.BackupDatabase(ctx, live, backup); err != nil {
		t.Fatal(err)
	}
	if err := storage.PrepareRestore(ctx, backup, copy); err != nil {
		t.Fatal(err)
	}
	restored, err := storage.Open(ctx, copy)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	agentToken, generation, err := restored.IssueAgentCredential(ctx, "agent", time.Hour)
	if err != nil || generation != 1 {
		t.Fatalf("reissue restored credential: generation=%d err=%v", generation, err)
	}

	book, err := ledger.Open(ctx, filepath.Join(dir, "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer book.Close()
	now := time.Now()
	id := "after-backup"
	if _, err := book.Accept(ctx, ledger.Request{Execution: id, Target: "target",
		Digest: "sha256:" + strings.Repeat("a", 64), Expires: now.Add(time.Hour)}, now); err != nil {
		t.Fatal(err)
	}
	for _, state := range []deployment.State{deployment.Executing, deployment.Verifying, deployment.Succeeded} {
		if err := book.Record(ctx, id, state); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := book.Pending(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if err := book.Acknowledge(ctx, e.ID); err != nil {
			t.Fatal(err)
		}
	}

	srv := httptest.NewUnstartedServer(dispatch.Endpoint(ctx, restored, dispatch.EndpointOptions{}))
	srv.StartTLS()
	defer srv.Close()
	serverRoots := x509.NewCertPool()
	serverRoots.AddCert(srv.Certificate())
	var runs atomic.Int32
	runner := executor.Runner{Ledger: book, Apply: func(context.Context, executor.Plan) error {
		runs.Add(1)
		return nil
	}}
	client := session.Client{Session: session.Session{Ledger: book}, Interval: 20 * time.Millisecond,
		Dial: func(ctx context.Context) (*agentwire.Channel, error) {
			return agentwire.DialCredential(ctx, "wss"+strings.TrimPrefix(srv.URL, "https"), &tls.Config{RootCAs: serverRoots}, agentToken)
		},
		Execute: runner.RunAccepted,
	}
	done := make(chan error, 1)
	go func() { done <- client.Run(ctx) }()
	defer func() { cancel(); <-done }()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	artifact, err := release.Compile(ctx, []byte("services: {web: {image: 'nginx@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'}}"), nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := restored.PublishRelease(ctx, "project", "r1", artifact); err != nil {
		t.Fatal(err)
	}
	snapshot, err := artifact.Bind(release.Binding{Target: "target", ConfigVersion: "v1", HealthTimeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	var admitted string
	for {
		select {
		case <-ctx.Done():
			t.Fatal("restore did not reach verified post-release deployment")
		case <-tick.C:
			if admitted != "" {
				view, err := restored.Get(ctx, admitted)
				if err != nil {
					t.Fatal(err)
				}
				if view.State != deployment.Succeeded {
					continue
				}
				if runs.Load() != 1 {
					t.Fatalf("wrong number of Docker executions: %d", runs.Load())
				}
				return
			}
			status, err := restored.RecoveryStatus(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if status.InventoriedAgents == 0 {
				continue
			}
			if status.ObservedExecutions != 1 || status.Reports != 0 {
				t.Fatalf("expected only retained inventory, got %+v", status)
			}
			if runs.Load() != 0 {
				t.Fatal("recovery inventory started a historical deployment")
			}
			if err := restored.ReleaseRestoredTarget(ctx, status.Epoch, "project", "target", "operator", "inspected Docker state; no old task remains"); err != nil {
				t.Fatal(err)
			}
			admitted, err = restored.AdmitSnapshot(ctx, storage.Admission{
				ID: "fresh", Project: "project", Caller: "owner", Target: "target", Release: "r1", Key: "fresh",
			}, snapshot)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
}
