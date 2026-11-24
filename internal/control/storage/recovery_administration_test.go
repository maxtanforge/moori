package storage

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/maxtanforge/moori/internal/core/deployment"
	"github.com/maxtanforge/moori/internal/core/nodeops"
	"github.com/maxtanforge/moori/internal/core/release"
)

func TestRecoveryCompletesAdministrationAfterLastHistoricalRelease(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, filepath.Join(t.TempDir(), "control.db"))
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	const password = "initial password"
	_, err := s.InitializeAdmin(ctx, "admin", password, time.Hour)
	must(err)
	must(s.EnsureLocalExecutor(ctx))
	must(s.BindAgent(ctx, "remote", 1, [32]byte{1}))
	for _, target := range []string{"target", "other"} {
		must(s.RegisterAssignedTarget(ctx, "project", target, "remote", 1))
	}
	must(s.RegisterTarget(ctx, "project", "unassigned"))
	must(s.enterRecovery(ctx))
	epoch, err := s.RecoveryEpoch(ctx)
	must(err)
	session, err := s.LoginHuman(ctx, "admin", password, time.Hour)
	must(err)
	must(s.RecordInventory(ctx, epoch, "remote", 1, nil, true))
	must(s.ReleaseRestoredTarget(ctx, epoch, "project", "target", "operator", "inspected"))

	now := time.Now()
	must(s.RecordNodeOperationCapability(ctx, LocalExecutorID, 1, now))
	req := nodeops.Request{Action: nodeops.VolumeCreate, Name: "data"}
	reg := RegistryInput{Name: "private", Host: "registry.example.test"}
	target := TargetSetup{Project: "project", Target: "new-target", Local: true}
	if _, err := s.QueueNodeOperation(ctx, session, LocalExecutorID, 1, req, now); !errors.Is(err, ErrRecovery) {
		t.Fatalf("Docker operation with incomplete recovery: %v", err)
	}
	if _, err := s.CreateHumanRegistry(ctx, session, SecretKey{}, reg); !errors.Is(err, ErrRecovery) {
		t.Fatalf("registry created with incomplete recovery: %v", err)
	}
	if err := s.ProvisionHumanTarget(ctx, session, target); !errors.Is(err, ErrRecovery) {
		t.Fatalf("target onboarded with incomplete recovery: %v", err)
	}
	if err := s.RegisterAssignedTarget(ctx, "project", "cli", LocalExecutorID, 1); !errors.Is(err, ErrRecovery) {
		t.Fatalf("CLI target registered with incomplete recovery: %v", err)
	}

	must(s.ReleaseRestoredTarget(ctx, epoch, "project", "other", "operator", "inspected"))
	_, err = s.CreateHumanRegistry(ctx, session, SecretKey{}, reg)
	must(err)
	must(s.ProvisionHumanTarget(ctx, session, target))
	must(s.ProvisionHumanTarget(ctx, session, target))
	must(s.RegisterAssignedTarget(ctx, "project", "cli", LocalExecutorID, 1))
	must(s.RegisterTarget(ctx, "project", "bare"))
	must(s.AssignTarget(ctx, "bare", LocalExecutorID, 1))
	must(s.ReassignTarget(ctx, "project", "cli", LocalExecutorID, 1))

	// Credential rotation fences the affected placement and node mutations.
	must(s.BindAgent(ctx, "remote", 2, [32]byte{2}))
	must(s.RecordNodeOperationCapability(ctx, "remote", 2, now))
	if _, err := s.QueueNodeOperation(ctx, session, "remote", 2, req, now); !errors.Is(err, ErrRecovery) {
		t.Fatalf("Docker mutation crossed a stale historical placement: %v", err)
	}
	_, err = s.CreateHumanRegistry(ctx, session, SecretKey{}, RegistryInput{Name: "after-rotation", Host: "registry2.example.test"})
	must(err)
	op, err := s.QueueNodeOperation(ctx, session, LocalExecutorID, 1, req, now)
	must(err)
	claimed, _, err := s.NextNodeOperation(ctx, LocalExecutorID, 1, now)
	must(err)
	if claimed.Operation != op.ID {
		t.Fatalf("claimed %q, want %q", claimed.Operation, op.ID)
	}
	_, err = s.RunningNodeOperation(ctx, LocalExecutorID, 1, op.ID, now)
	must(err)
	must(s.CompleteNodeOperation(ctx, LocalExecutorID, 1, op.ID, nodeops.Succeeded, "", &nodeops.Result{}, now))

	r, err := release.Compile(ctx, []byte("services: {web: {image: 'nginx@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'}}"), nil, nil, nil)
	must(err)
	must(s.PublishRelease(ctx, "project", "release", r))
	admit := func(target string) error {
		snapshot, err := r.Bind(release.Binding{Target: target, ConfigVersion: "v1", HealthTimeout: time.Minute})
		must(err)
		_, err = s.AdmitSnapshot(ctx, Admission{ID: target, Project: "project", Caller: "ci", Key: target, Target: target, Release: "release"}, snapshot)
		return err
	}
	if err := admit("target"); !errors.Is(err, ErrRecovery) {
		t.Fatalf("historical target escaped stale assignment fence: %v", err)
	}
	must(admit("new-target"))
	offer, err := s.NextOffer(ctx, LocalExecutorID, 1, now, time.Minute)
	must(err)
	if offer.ID != "new-target" {
		t.Fatalf("unexpected offer: %+v", offer)
	}
	must(s.ApplyEvent(ctx, Event{ID: "fresh", Execution: offer.Execution, Agent: LocalExecutorID, Generation: 1, Sequence: 1, State: deployment.Executing}))
	must(s.ApplyEvent(ctx, Event{ID: "late", Execution: "unknown", Agent: "remote", Generation: 2, Sequence: 1, State: deployment.Executing}))
	status, err := s.RecoveryStatus(ctx)
	must(err)
	if status.Epoch != epoch || status.Reports != 1 || status.PendingTargets != 0 {
		t.Fatalf("lost original epoch or late evidence: %+v", status)
	}

	must(s.enterRecovery(ctx))
	session, err = s.LoginHuman(ctx, "admin", password, time.Hour)
	must(err)
	if err := admit("new-target"); !errors.Is(err, ErrRecovery) {
		t.Fatalf("second restore did not fence the new target: %v", err)
	}
	if err := s.ProvisionHumanTarget(ctx, session, TargetSetup{Project: "project", Target: "later", Local: true}); !errors.Is(err, ErrRecovery) {
		t.Fatalf("second restore did not restore the global fence: %v", err)
	}
}

func TestRestoreRetiresNodeOperationsAndKeepsRunningMutationFence(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, filepath.Join(t.TempDir(), "control.db"))
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	const password = "initial password"
	session, err := s.InitializeAdmin(ctx, "admin", password, time.Hour)
	must(err)
	must(s.EnsureLocalExecutor(ctx))
	now := time.Now()
	must(s.RecordNodeOperationCapability(ctx, LocalExecutorID, 1, now))
	req := nodeops.Request{Action: nodeops.VolumeCreate, Name: "data"}
	running, err := s.QueueNodeOperation(ctx, session, LocalExecutorID, 1, req, now)
	must(err)
	_, deadline, err := s.NextNodeOperation(ctx, LocalExecutorID, 1, now)
	must(err)
	queued, err := s.QueueNodeOperation(ctx, session, LocalExecutorID, 1, req, now)
	must(err)

	must(s.enterRecovery(ctx))
	session, err = s.LoginHuman(ctx, "admin", password, time.Hour)
	must(err)
	must(s.CompleteNodeOperation(ctx, LocalExecutorID, 1, running.ID, nodeops.Succeeded, "", &nodeops.Result{}, now))
	for _, tc := range []struct {
		id    string
		state nodeops.State
	}{{running.ID, nodeops.Uncertain}, {queued.ID, nodeops.Failed}} {
		result, err := s.GetNodeOperation(ctx, session, LocalExecutorID, tc.id, now)
		must(err)
		if result.State != tc.state {
			t.Fatalf("restored operation state: got %s, want %s", result.State, tc.state)
		}
	}
	if _, err := s.RunningNodeOperation(ctx, LocalExecutorID, 1, running.ID, now); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("restored claim reachable: %v", err)
	}
	if _, err := s.QueueNodeOperation(ctx, session, LocalExecutorID, 1, req, now); !errors.Is(err, ErrNodeOperationsUnavailable) {
		t.Fatalf("backup capability considered current: %v", err)
	}
	must(s.RecordNodeOperationCapability(ctx, LocalExecutorID, 1, now))
	fresh, err := s.QueueNodeOperation(ctx, session, LocalExecutorID, 1, req, now)
	must(err)

	must(s.RegisterAssignedTarget(ctx, "project", "target", LocalExecutorID, 1))
	snap := testSnapshot(t, s, "v1")
	_, err = s.AdmitSnapshot(ctx, Admission{ID: "deploy", Project: "project", Caller: "ci", Key: "deploy", Target: "target", Release: "release"}, snap)
	must(err)
	if _, err := s.NextOffer(ctx, LocalExecutorID, 1, now, time.Minute); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deployment bypassed restored Docker mutation fence: %v", err)
	}
	if _, _, err := s.NextNodeOperation(ctx, LocalExecutorID, 1, now); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("operation bypassed restored Docker mutation fence: %v", err)
	}
	got, _, err := s.NextNodeOperation(ctx, LocalExecutorID, 1, deadline.Add(time.Millisecond))
	must(err)
	if got.Operation != fresh.ID {
		t.Fatalf("claimed %q after restore, want %q", got.Operation, fresh.ID)
	}
}

func TestRestoreReleasesLockedTargetWithoutExistingAssignment(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, filepath.Join(t.TempDir(), "control.db"))
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.BindAgent(ctx, "remote", 1, [32]byte{1}))
	must(s.RegisterTarget(ctx, "project", "target"))
	snapshot := testSnapshot(t, s, "v1")
	_, err := s.AdmitSnapshot(ctx, Admission{ID: "old", Project: "project", Caller: "ci", Key: "old", Target: "target", Release: "release"}, snapshot)
	must(err)
	must(s.Claim(ctx, "old", "previous", "remote", 1, time.Now().Add(-time.Minute)))
	must(s.enterRecovery(ctx))
	view, err := s.RecoveryStatus(ctx)
	must(err)
	if view.PendingTargets != 1 {
		t.Fatalf("target with locked attempt escaped recovery: %+v", view)
	}
	must(s.RecordInventory(ctx, view.Epoch, "remote", 1, nil, true))
	must(s.ReleaseRestoredTarget(ctx, view.Epoch, "project", "target", "operator", "inspected"))
	must(s.ReleaseRestoredTarget(ctx, view.Epoch, "project", "target", "operator", "inspected"))
	_, err = s.AdmitSnapshot(ctx, Admission{ID: "new", Project: "project", Caller: "ci", Key: "new", Target: "target", Release: "release"}, snapshot)
	must(err)
	offer, err := s.NextOffer(ctx, "remote", 1, time.Now(), time.Minute)
	must(err)
	if offer.ID != "new" {
		t.Fatalf("recovered deployment placement unusable: %+v", offer)
	}
}

func TestLateRecoveryEvidenceFencesNodeMutationsAcrossGenerations(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, filepath.Join(t.TempDir(), "control.db"))
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	const password = "initial password"
	_, err := s.InitializeAdmin(ctx, "admin", password, time.Hour)
	must(err)
	must(s.EnsureLocalExecutor(ctx))
	must(s.BindAgent(ctx, "remote", 1, [32]byte{1}))
	must(s.RegisterAssignedTarget(ctx, "project", "target", "remote", 1))
	must(s.enterRecovery(ctx))
	session, err := s.LoginHuman(ctx, "admin", password, time.Hour)
	must(err)
	epoch, err := s.RecoveryEpoch(ctx)
	must(err)
	must(s.RecordInventory(ctx, epoch, "remote", 1, nil, true))
	must(s.ReleaseRestoredTarget(ctx, epoch, "project", "target", "operator", "inspected"))
	now := time.Now()
	must(s.RecordNodeOperationCapability(ctx, "remote", 1, now))
	must(s.RecordNodeOperationCapability(ctx, LocalExecutorID, 1, now))

	mutation := nodeops.Request{Action: nodeops.VolumeCreate, Name: "data"}
	saved, err := s.QueueNodeOperation(ctx, session, "remote", 1, mutation, now)
	must(err)
	event := Event{ID: "late-active", Execution: "missing-from-backup", Agent: "remote", Generation: 1, Sequence: 1, State: deployment.Executing}
	must(s.ApplyEvent(ctx, event))
	if _, err := s.QueueNodeOperation(ctx, session, "remote", 1, mutation, now); !errors.Is(err, ErrTargetBusy) {
		t.Fatalf("late active work did not fence mutation queue: %v", err)
	}
	if _, _, err := s.NextNodeOperation(ctx, "remote", 1, now); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("late active work did not fence existing mutation: %v", err)
	}
	_, err = s.QueueNodeOperation(ctx, session, LocalExecutorID, 1, mutation, now)
	must(err)
	readOnly, err := s.QueueNodeOperation(ctx, session, "remote", 1, nodeops.Request{Action: nodeops.VolumeInspect, Target: "data"}, now)
	must(err)
	got, _, err := s.NextNodeOperation(ctx, "remote", 1, now)
	must(err)
	if got.Operation != readOnly.ID {
		t.Fatalf("read-only operation blocked: %+v", got)
	}
	must(s.CompleteNodeOperation(ctx, "remote", 1, readOnly.ID, nodeops.Succeeded, "", &nodeops.Result{}, now))
	event.ID, event.Sequence, event.State = "late-terminal", 2, deployment.Succeeded
	must(s.ApplyEvent(ctx, event))
	got, _, err = s.NextNodeOperation(ctx, "remote", 1, now)
	must(err)
	if got.Operation != saved.ID {
		t.Fatalf("terminal evidence did not clear mutation fence: %+v", got)
	}

	other := Event{ID: "pre-rotation-active", Execution: "another-missing-run", Agent: "remote", Generation: 1, Sequence: 1, State: deployment.Executing}
	must(s.ApplyEvent(ctx, other))
	if _, err := s.RunningNodeOperation(ctx, "remote", 1, saved.ID, now); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("late activity left claimed mutation credentials accessible: %v", err)
	}
	// A second node with no target placement isolates the evidence fence from
	// the stale-placement fence.
	must(s.BindAgent(ctx, "other", 1, [32]byte{2}))
	other.ID, other.Agent = "other-active", "other"
	must(s.ApplyEvent(ctx, other))
	must(s.BindAgent(ctx, "other", 2, [32]byte{3}))
	must(s.RecordNodeOperationCapability(ctx, "other", 2, now))
	other.ID, other.Generation, other.Sequence, other.State = "other-new-terminal", 2, 2, deployment.Succeeded
	must(s.ApplyEvent(ctx, other))
	if _, err := s.QueueNodeOperation(ctx, session, "other", 2, mutation, now); !errors.Is(err, ErrTargetBusy) {
		t.Fatalf("another generation's terminal evidence cleared an old mutation fence: %v", err)
	}
}

func TestRecoveryReleaseRebindsRotatedPlacementAfterCurrentInventory(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	live, backup, restored := filepath.Join(dir, "live.db"), filepath.Join(dir, "backup.db"), filepath.Join(dir, "restored.db")
	s := openTestStore(t, live)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	_, generation, err := s.IssueAgentCredential(ctx, "remote", time.Hour)
	must(err)
	must(s.RegisterAssignedTarget(ctx, "project", "target", "remote", generation))
	snap := testSnapshot(t, s, "v1")
	_, err = s.AdmitSnapshot(ctx, Admission{ID: "old", Project: "project", Caller: "ci", Key: "old", Target: "target", Release: "release"}, snap)
	must(err)
	now := time.Now()
	offer, err := s.NextOffer(ctx, "remote", generation, now, time.Minute)
	must(err)
	_, rotated, err := s.IssueAgentCredential(ctx, "remote", time.Hour)
	must(err)
	must(BackupDatabase(ctx, live, backup))
	must(PrepareRestore(ctx, backup, restored))

	r := openTestStore(t, restored)
	_, recovered, err := r.IssueAgentCredential(ctx, "remote", time.Hour)
	must(err)
	if recovered != rotated {
		t.Fatalf("credential restore changed generation: got %d, want %d", recovered, rotated)
	}
	epoch, err := r.RecoveryEpoch(ctx)
	must(err)
	releaseTarget := func() error {
		return r.ReleaseRestoredTarget(ctx, epoch, "project", "target", "operator", "inspected")
	}
	if err := releaseTarget(); !errors.Is(err, ErrRecovery) {
		t.Fatalf("release without latest inventory: %v", err)
	}
	active := deployment.Evidence{Execution: offer.Execution, Target: "target", Digest: snap.Digest(), Sequence: 1, State: deployment.Executing}
	must(r.RecordInventory(ctx, epoch, "remote", recovered, []deployment.Evidence{active}, true))
	if err := releaseTarget(); !errors.Is(err, ErrTargetBusy) {
		t.Fatalf("active runtime evidence did not block rebind: %v", err)
	}
	active.Sequence, active.State = 2, deployment.Succeeded
	must(r.RecordInventory(ctx, epoch, "remote", recovered, []deployment.Evidence{active}, true))
	if err := releaseTarget(); !errors.Is(err, ErrTargetBusy) {
		t.Fatalf("still-valid original offer did not block rebind: %v", err)
	}
	_, err = r.db.ExecContext(ctx, "UPDATE deployments SET expires_ms=? WHERE id='old'", now.Add(-time.Minute).UnixMilli())
	must(err)
	must(releaseTarget())
	must(releaseTarget())
	old, err := r.Get(ctx, "old")
	must(err)
	if old.State != deployment.Abandoned || old.Generation != generation {
		t.Fatalf("release lost original attempt identity: %+v", old)
	}
	_, err = r.AdmitSnapshot(ctx, Admission{ID: "new", Project: "project", Caller: "ci", Key: "new", Target: "target", Release: "release"}, snap)
	must(err)
	offer, err = r.NextOffer(ctx, "remote", recovered, now, time.Minute)
	must(err)
	if offer.ID != "new" || offer.Generation != recovered {
		t.Fatalf("release did not restore dispatch: %+v", offer)
	}
}
