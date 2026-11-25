package localexec

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maxtanforge/moori/internal/control/storage"
	"github.com/maxtanforge/moori/internal/core/deployment"
	"github.com/maxtanforge/moori/internal/core/release"
	"github.com/maxtanforge/moori/internal/node/executor"
	"github.com/maxtanforge/moori/internal/node/ledger"
	"github.com/maxtanforge/moori/internal/platform/execlog"
	"github.com/maxtanforge/moori/internal/transport/agentwire"
)

func TestInProcessDeploymentReplaysLostReplyWithoutRerunning(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	dir := t.TempDir()
	s, err := storage.Open(ctx, filepath.Join(dir, "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	book, err := ledger.Open(ctx, filepath.Join(dir, "local.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer book.Close()
	token, err := s.InitializeAdmin(ctx, "admin", "initial password", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureLocalExecutor(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.ProvisionHumanTarget(ctx, token, storage.TargetSetup{Project: "app", Target: "production", Local: true}); err != nil {
		t.Fatal(err)
	}
	artifact, err := release.Compile(ctx, []byte("services: {web: {image: 'nginx@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'}}"), nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PublishRelease(ctx, "app", "v1", artifact); err != nil {
		t.Fatal(err)
	}
	id, err := s.AdmitHumanRelease(ctx, token, storage.Admission{ID: "first", Project: "app", Target: "production", Release: "v1", Key: "first"})
	if err != nil {
		t.Fatal(err)
	}
	spool, err := execlog.Open(filepath.Join(dir, "spool"), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	logs, err := execlog.Open(filepath.Join(dir, "logs"), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	var runs atomic.Int32
	var lost atomic.Bool
	started := make(chan struct{})
	client := NewClient(Options{Store: s, Ledger: book, Logs: logs, Spool: spool, Apply: func(ctx context.Context, p executor.Plan) error {
		if runs.Add(1) > 1 {
			close(started)
			<-ctx.Done()
			return executor.ErrOutcomeUnknown
		}
		if p.Project == "" || p.Compose == "" {
			return errors.New("empty plan")
		}
		_, _ = io.WriteString(p.Output, "local deployment completed")
		return nil
	}})
	client.Interval = 10 * time.Millisecond
	direct := client.Direct
	client.Direct = func(ctx context.Context, m agentwire.Message) (agentwire.Message, error) {
		reply, err := direct(ctx, m)
		var report agentwire.Report
		if err == nil && m.Kind == agentwire.KindReport && agentwire.Decode(m, agentwire.KindReport, &report) == nil && report.State == deployment.Succeeded && lost.CompareAndSwap(false, true) {
			return agentwire.Message{}, errors.New("lost committed reply")
		}
		return reply, err
	}
	done := make(chan error, 1)
	go func() { done <- client.Run(ctx) }()
	defer func() { cancel(); <-done }()
	completed := false
	for ctx.Err() == nil {
		view, err := s.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		pending, err := book.Pending(ctx, 20)
		if err != nil {
			t.Fatal(err)
		}
		if lost.Load() && view.State == deployment.Succeeded && len(pending) == 0 {
			if runs.Load() != 1 {
				t.Fatal("duplicate Docker execution", runs.Load())
			}
			if output, err := logs.Read(id); err == nil && output.Text == "local deployment completed" {
				completed = true
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !completed {
		t.Fatal("local deployment or output did not finish")
	}
	second, err := s.AdmitHumanRelease(ctx, token, storage.Admission{ID: "second", Project: "app", Target: "production", Release: "v1", Key: "second"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("second execution did not start")
	}
	if _, err := s.RequestHumanCancel(ctx, token, "app", second, "stop deployment"); err != nil {
		t.Fatal(err)
	}
	for ctx.Err() == nil {
		view, err := s.Get(ctx, second)
		if err != nil {
			t.Fatal(err)
		}
		if view.State == deployment.Reconciling {
			if runs.Load() != 2 {
				t.Fatal("unexpected executions", runs.Load())
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("cancellation did not reach the local worker")
}
