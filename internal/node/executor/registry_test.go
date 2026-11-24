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

func TestRegistryPullPrecedesCompose(t *testing.T) {
	ctx := context.Background()
	for _, fail := range []bool{false, true} {
		l, err := ledger.Open(ctx, filepath.Join(t.TempDir(), "agent.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer l.Close()
		const image = "registry.example/team/app@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		r, err := release.Compile(ctx, []byte("services: {one: {image: '"+image+"'}, two: {image: '"+image+"'}}"), nil, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		snap, err := r.Bind(release.Binding{Target: "prod", ConfigVersion: "v1", HealthTimeout: time.Minute})
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now()
		if _, err := l.Accept(ctx, ledger.Request{Execution: "execute", Target: "prod", Digest: snap.Digest(), Expires: now.Add(time.Hour)}, now); err != nil {
			t.Fatal(err)
		}
		var pulled, applied int
		cred := registry.Credential{Username: "robot", Password: "token", ServerAddress: "registry.example"}
		runner := Runner{Ledger: l, FetchRegistry: func(_ context.Context, id, digest string) (map[string]registry.Credential, error) {
			if id != "execute" || digest != snap.Digest() {
				t.Fatal("registry fetch escaped the accepted execution")
			}
			return map[string]registry.Credential{image: cred}, nil
		}, PullImage: func(_ context.Context, ref, platform string, auth registry.Credential) error {
			if ref != image || auth != cred || applied != 0 {
				t.Fatal("pull used unexpected reference, credential, or order")
			}
			pulled++
			if fail {
				return errors.New("unavailable")
			}
			return nil
		}, Apply: func(_ context.Context, plan Plan) error {
			if !plan.ImagesReady || pulled != 1 {
				t.Fatal("Compose ran without verified pulls")
			}
			applied++
			return nil
		}}
		err = runner.RunAccepted(ctx, "execute", snap.JSON())
		entry, getErr := l.Get(ctx, "execute")
		if getErr != nil || pulled != 1 {
			t.Fatalf("pull count %d, ledger error %v", pulled, getErr)
		}
		if fail && (err == nil || applied != 0 || entry.State != deployment.Failed) || !fail && (err != nil || applied != 1 || entry.State != deployment.Succeeded) {
			t.Fatalf("failure mode %v: err %v, apply count %d, state %v", fail, err, applied, entry.State)
		}
	}
}
