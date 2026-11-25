package executor

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/maxtanforge/moori/internal/core/deployment"
	"github.com/maxtanforge/moori/internal/core/registry"
	"github.com/maxtanforge/moori/internal/core/release"
	"github.com/maxtanforge/moori/internal/node/ledger"
)

func TestCanceledImagePullDoesNotStartCompose(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	l, err := ledger.Open(ctx, filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	artifact, err := release.Compile(ctx, []byte("services: {web: {image: 'busybox@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'}}"), nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := artifact.Bind(release.Binding{Target: "prod", ConfigVersion: "v1", HealthTimeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if _, err := l.Accept(ctx, ledger.Request{Execution: "execute", Target: "prod", Digest: snap.Digest(), Expires: now.Add(time.Hour)}, now); err != nil {
		t.Fatal(err)
	}
	runner := Runner{Ledger: l, FetchRegistry: func(context.Context, string, string) (map[string]registry.Credential, error) { return nil, nil },
		PullImage: func(ctx context.Context, _ string, _ string, _ registry.Credential) error { cancel(); return ctx.Err() },
		Apply:     func(context.Context, Plan) error { t.Fatal("Compose executed after cancellation"); return nil },
	}
	if err := runner.RunAccepted(ctx, "execute", snap.JSON()); err != nil {
		t.Fatal(err)
	}
	entry, err := l.Get(context.Background(), "execute")
	if err != nil || entry.State != deployment.Canceled {
		t.Fatal(entry.State, err)
	}
}
