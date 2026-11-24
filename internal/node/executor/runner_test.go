package executor

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/maxtanforge/moori/internal/core/deployment"
	"github.com/maxtanforge/moori/internal/core/release"
	"github.com/maxtanforge/moori/internal/node/ledger"
)

func TestAcceptedExecutionNeverLaunchesTwice(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		name := "success"
		if unknown {
			name = "interrupted"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			l, err := ledger.Open(ctx, filepath.Join(t.TempDir(), "agent.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer l.Close()
			r, err := release.Compile(ctx, []byte("services: {web: {image: 'nginx@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'}}"), nil, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := r.Bind(release.Binding{Target: "target", ConfigVersion: "v1", HealthTimeout: time.Minute})
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			if _, err := l.Accept(ctx, ledger.Request{Execution: "attempt", Target: "target", Digest: snapshot.Digest(), Expires: now.Add(time.Hour)}, now); err != nil {
				t.Fatal(err)
			}
			calls := 0
			runner := Runner{Ledger: l, Apply: func(ctx context.Context, plan Plan) error {
				calls++
				entry, err := l.Get(ctx, "attempt")
				if err != nil || entry.State != deployment.Executing {
					t.Fatalf("launch before durable execution: %+v %v", entry, err)
				}
				if plan.Timeout != time.Minute || plan.Project == "" {
					t.Fatalf("invalid plan: %+v", plan)
				}
				if unknown {
					return ErrOutcomeUnknown
				}
				return nil
			}}
			if err := runner.RunAccepted(ctx, "attempt", snapshot.JSON()+" "); err == nil || calls != 0 {
				t.Fatal("changed payload reached executor")
			}
			err = runner.RunAccepted(ctx, "attempt", snapshot.JSON())
			if unknown && !errors.Is(err, ErrOutcomeUnknown) || !unknown && err != nil {
				t.Fatalf("run: %v", err)
			}
			if err := runner.RunAccepted(ctx, "attempt", snapshot.JSON()); err == nil || calls != 1 {
				t.Fatalf("retry executed twice: %d %v", calls, err)
			}
			entry, err := l.Get(ctx, "attempt")
			want := deployment.Succeeded
			if unknown {
				want = deployment.Reconciling
			}
			if err != nil || entry.State != want {
				t.Fatalf("entry=%+v err=%v want=%s", entry, err, want)
			}
		})
	}
}
