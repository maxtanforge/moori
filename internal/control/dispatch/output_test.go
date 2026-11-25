package dispatch

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/maxtanforge/moori/internal/control/storage"
	"github.com/maxtanforge/moori/internal/core/release"
	"github.com/maxtanforge/moori/internal/platform/execlog"
	"github.com/maxtanforge/moori/internal/transport/agentwire"
)

func TestOutputSeparatesAuthorizationStorageFailureAndRetry(t *testing.T) {
	ctx := context.Background()
	s, err := storage.Open(ctx, filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.RegisterTarget(ctx, "project", "target"))
	must(s.AssignTarget(ctx, "target", "agent", 1))
	artifact, err := release.Compile(ctx, []byte("services: {web: {image: 'nginx@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'}}"), nil, nil, nil)
	must(err)
	must(s.PublishRelease(ctx, "project", "release", artifact))
	snapshot, err := artifact.Bind(release.Binding{Target: "target", ConfigVersion: "v1", HealthTimeout: time.Minute})
	must(err)
	_, err = s.AdmitSnapshot(ctx, storage.Admission{ID: "deploy", Project: "project", Caller: "ci", Key: "key", Target: "target", Release: "release"}, snapshot)
	must(err)
	_, err = s.NextOffer(ctx, "agent", 1, time.Now(), time.Minute)
	must(err)
	dir := filepath.Join(t.TempDir(), "logs")
	logs, err := execlog.Open(dir, 1<<20)
	must(err)
	service := Session{Store: s, Logs: logs}
	entry := execlog.Entry{Execution: "deploy", Text: "test output"}
	m, err := agentwire.Encode(agentwire.KindOutput, entry)
	must(err)
	send := func(peer string) agentwire.OutputAck {
		t.Helper()
		reply, err := service.output(ctx, agentwire.Peer{ID: peer, Generation: 1}, m)
		must(err)
		var ack agentwire.OutputAck
		must(agentwire.Decode(reply, agentwire.KindOutputAck, &ack))
		return ack
	}
	if ack := send("other"); ack.Retained || ack.Retry {
		t.Fatal("unauthorized request not rejected", ack)
	}
	must(os.Remove(dir)) // Simulate storage becoming unavailable after startup.
	if ack := send("agent"); ack.Retained || !ack.Retry {
		t.Fatal("storage error not retryable", ack)
	}
	must(os.Mkdir(dir, 0700))
	if ack := send("agent"); !ack.Retained || ack.Retry {
		t.Fatal("retry did not retain", ack)
	}
	got, err := logs.Read("deploy")
	must(err)
	if got != entry {
		t.Fatal(got)
	}
	must(s.Close())
	if ack := send("agent"); ack.Retained || !ack.Retry {
		t.Fatal("database error not retryable", ack)
	}
}
