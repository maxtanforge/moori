package dispatch_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maxtanforge/moori/internal/control/dispatch"
	"github.com/maxtanforge/moori/internal/control/observability"
	"github.com/maxtanforge/moori/internal/control/storage"
	"github.com/maxtanforge/moori/internal/core/deployment"
	"github.com/maxtanforge/moori/internal/core/release"
	"github.com/maxtanforge/moori/internal/core/telemetry"
	"github.com/maxtanforge/moori/internal/node/executor"
	"github.com/maxtanforge/moori/internal/node/ledger"
	"github.com/maxtanforge/moori/internal/node/session"
	"github.com/maxtanforge/moori/internal/platform/execlog"
	"github.com/maxtanforge/moori/internal/transport/agentwire"
)

func TestRunningCancellationSignalPreservesUncertainTarget(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	dir := t.TempDir()
	s, err := storage.Open(ctx, filepath.Join(dir, "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	agentToken, _, err := s.IssueAgentCredential(ctx, "agent", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RegisterAssignedTarget(ctx, "project", "target", "agent", 1); err != nil {
		t.Fatal(err)
	}
	artifact, err := release.Compile(ctx, []byte("services: {web: {image: 'nginx@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'}}"), nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PublishRelease(ctx, "project", "r1", artifact); err != nil {
		t.Fatal(err)
	}
	snap, err := artifact.Bind(release.Binding{Target: "target", ConfigVersion: "v1", HealthTimeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.AdmitSnapshot(ctx, storage.Admission{ID: "deploy", Project: "project", Caller: "owner", Target: "target", Release: "r1", Key: "deploy"}, snap)
	if err != nil {
		t.Fatal(err)
	}
	book, err := ledger.Open(ctx, filepath.Join(dir, "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer book.Close()
	controlLogs, err := execlog.Open(filepath.Join(dir, "control-logs"), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	nodeLogs, err := execlog.Open(filepath.Join(dir, "agent-logs"), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	health, err := observability.Open(ctx, filepath.Join(dir, "metrics.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer health.Close()
	srv := httptest.NewUnstartedServer(dispatch.Endpoint(ctx, s, dispatch.EndpointOptions{Logs: controlLogs, Health: health}))
	srv.StartTLS()
	defer srv.Close()
	serverRoots := x509.NewCertPool()
	serverRoots.AddCert(srv.Certificate())
	started := make(chan struct{}, 1)
	interrupted := make(chan struct{}, 1)
	runner := executor.Runner{Ledger: book, Logs: nodeLogs, Apply: func(runCtx context.Context, plan executor.Plan) error {
		started <- struct{}{}
		<-runCtx.Done()
		_, _ = plan.Output.Write([]byte("Docker CLI canceled"))
		interrupted <- struct{}{}
		return executor.ErrOutcomeUnknown
	}}
	client := session.Client{Session: session.Session{Ledger: book}, Execute: runner.RunAccepted, Interval: 20 * time.Millisecond, Logs: nodeLogs,
		Collect: func() (telemetry.Sample, error) {
			return telemetry.Sample{MemoryBytes: 100, MemoryAvailableBytes: 50, DiskBytes: 100, DiskAvailableBytes: 5}, nil
		},
		Dial: func(ctx context.Context) (*agentwire.Channel, error) {
			return agentwire.DialCredential(ctx, "wss"+strings.TrimPrefix(srv.URL, "https"), &tls.Config{RootCAs: serverRoots}, agentToken)
		},
	}
	done := make(chan error, 1)
	go func() { done <- client.Run(ctx) }()
	defer func() { cancel(); <-done }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("deployment did not start")
	}
	if result, err := s.RequestCancel(ctx, id, "operator", "test running cancellation"); err != nil || result.Decision != deployment.RequestAgentCancel {
		t.Fatalf("cancellation not persisted: %+v %v", result, err)
	}
	select {
	case <-interrupted:
	case <-ctx.Done():
		t.Fatal("running worker did not receive cancellation")
	}
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			t.Fatal("control did not retain reconciliation hold")
		case <-tick.C:
			view, err := s.Get(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if view.State != deployment.Reconciling {
				continue
			}
			entry, err := controlLogs.Read(view.Execution)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil || !strings.Contains(entry.Text, "Docker CLI canceled") {
				t.Fatalf("deployment output not retained: %+v %v", entry, err)
			}
			sample, err := health.Health(ctx, "agent", 1, time.Now())
			if err != nil || sample.Contact != "online" || sample.Metrics != "current" ||
				len(sample.Alerts) != 1 || sample.Alerts[0] != "disk_low" {
				t.Fatalf("authenticated resource sample not stored: %+v %v", sample, err)
			}
			if _, err := s.AdmitSnapshot(ctx, storage.Admission{ID: "next", Project: "project", Caller: "owner", Target: "target", Release: "r1", Key: "next"}, snap); err != nil {
				t.Fatal(err)
			}
			if _, err := s.NextOffer(ctx, "agent", 1, time.Now(), time.Minute); !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("uncertain target became dispatchable: %v", err)
			}
			return
		}
	}
}
