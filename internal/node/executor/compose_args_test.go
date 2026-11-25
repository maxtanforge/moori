package executor

import (
	"reflect"
	"slices"
	"testing"
	"time"
)

func TestComposeReconcilesOrphansWithinTargetProject(t *testing.T) {
	want := []string{"--host", "unix:///var/run/docker.sock", "compose", "--env-file", "/dev/null", "-p", "moori-target", "-f", "-", "up", "--detach", "--remove-orphans", "--no-build", "--pull", "always", "--wait", "--wait-timeout", "2"}
	if got := composeArgs(Plan{Project: "moori-target", Timeout: time.Second + time.Millisecond}); !reflect.DeepEqual(got, want) {
		t.Fatalf("Compose reconciliation arguments = %v, want %v", got, want)
	}
}

func TestComposeForceRecreateRequiresExplicitPlan(t *testing.T) {
	plan := Plan{Project: "moori-target", Timeout: time.Minute}
	if slices.Contains(composeArgs(plan), "--force-recreate") {
		t.Fatal("ordinary update forces a rebuild")
	}
	plan.ForceRecreate = true
	if !slices.Contains(composeArgs(plan), "--force-recreate") {
		t.Fatal("redeploy did not pass the force-recreate flag")
	}
}
