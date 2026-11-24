package executor

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/maxtanforge/moori/internal/core/deployment"
	"github.com/maxtanforge/moori/internal/core/registry"
	"github.com/maxtanforge/moori/internal/core/release"
	"github.com/maxtanforge/moori/internal/node/ledger"
)

func TestUnavailableRegistryProviderDoesNotBlockAcceptedQueue(t *testing.T) {
	ctx := context.Background()
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
	var calls int
	runner := Runner{Ledger: l, FetchRegistry: func(context.Context, string, string) (map[string]registry.Credential, error) {
		calls++
		return nil, registry.ErrRetry
	},
		PullImage: func(context.Context, string, string, registry.Credential) error {
			t.Fatal("image was pulled without registry authorization")
			return nil
		},
		Apply: func(context.Context, Plan) error {
			t.Fatal("Compose executed after credential provider failure")
			return nil
		},
	}
	if err := runner.RunAccepted(ctx, "execute", snap.JSON()); !errors.Is(err, registry.ErrRetry) {
		t.Fatal(err)
	}
	entry, err := l.Get(ctx, "execute")
	if err != nil || entry.State != deployment.Failed || calls != 3 {
		t.Fatalf("retry attempts=%d, state=%v, err=%v", calls, entry.State, err)
	}
}
