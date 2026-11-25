package storage

import (
	"context"
	"crypto/sha256"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestDeleteAgentFencesAllEnrollmentPaths(t *testing.T) {
	ctx := t.Context()
	s := openTestStore(t, filepath.Join(t.TempDir(), "db"))
	admin, err := s.InitializeAdmin(ctx, "admin", "initial password", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	token, gen, err := s.IssueAgentCredential(ctx, "node", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	live, stop, err := s.AgentContext(ctx, "node", gen)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	if _, err := s.db.ExecContext(ctx, `INSERT INTO node_operations(id,agent,generation,actor,action,request,mutation,state,created_ms,expires_ms)
 VALUES ('op','node',1,'admin','test','{}',0,'running',1,?)`, time.Now().Add(time.Minute).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteHumanAgent(ctx, admin, "node", gen); !errors.Is(err, ErrTargetBusy) {
		t.Fatal(err)
	}
	if live.Err() != nil {
		t.Fatal("conflicting request canceled active connection")
	}
	if _, err := s.db.ExecContext(ctx, "UPDATE node_operations SET state='succeeded' WHERE id='op'"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteHumanAgent(ctx, admin, "node", gen); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(live.Err(), context.Canceled) {
		t.Fatal("live connection survived delete")
	}
	if _, _, err := s.AgentContext(ctx, "node", gen); err == nil {
		t.Fatal("deleted connection registered")
	}
	if err := s.BindAgent(ctx, "node", gen, sha256.Sum256([]byte("old"))); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	certToken, err := s.IssueEnrollment(ctx, "node", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.CompleteEnrollment(ctx, certToken, [32]byte{2}, time.Now(), func(agent string, replacement int64, lineage string) ([]byte, error) {
		if replacement != gen+1 {
			t.Fatal("managed certificate reused retired generation", replacement)
		}
		return []byte("certificate"), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteHumanAgent(ctx, admin, "node", gen+1); err != nil {
		t.Fatal(err)
	}
	_, fresh, err := s.IssueAgentCredential(ctx, "node", time.Hour)
	if err != nil || fresh != gen+2 {
		t.Fatal(fresh, err)
	}
	if _, _, err := s.AuthenticateAgentCredential(ctx, token); err == nil {
		t.Fatal("old bearer accepted")
	}
	var deletes, operations int
	if err := s.db.QueryRow("SELECT count(*) FROM project_audit WHERE action='node.deleted' AND subject='node'").Scan(&deletes); err != nil || deletes != 2 {
		t.Fatal(deletes, err)
	}
	if err := s.db.QueryRow("SELECT count(*) FROM node_operations WHERE id='op' AND state='succeeded'").Scan(&operations); err != nil || operations != 1 {
		t.Fatal(operations, err)
	}
}

func TestDeleteAgentHonorsRecoveryAndAllowsReassignment(t *testing.T) {
	ctx := t.Context()
	s := openTestStore(t, filepath.Join(t.TempDir(), "db"))
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err := s.InitializeAdmin(ctx, "admin", "initial password", time.Hour)
	must(err)
	must(s.BindAgent(ctx, "node", 1, [32]byte{1}))
	must(s.RegisterAssignedTarget(ctx, "project", "target", "node", 1))
	must(s.enterRecovery(ctx))
	admin, err := s.LoginHuman(ctx, "admin", "initial password", time.Hour)
	must(err)
	if err := s.DeleteHumanAgent(ctx, admin, "node", 1); !errors.Is(err, ErrRecovery) {
		t.Fatal(err)
	}
	epoch, err := s.RecoveryEpoch(ctx)
	must(err)
	must(s.RecordInventory(ctx, epoch, "node", 1, nil, true))
	must(s.ReleaseRestoredTarget(ctx, epoch, "project", "target", "operator", "Docker inspected"))
	must(s.DeleteHumanAgent(ctx, admin, "node", 1))
	must(s.BindAgent(ctx, "node", 2, [32]byte{2}))
	if _, _, err := s.ReadHumanTargetAgent(ctx, admin, "project", "target"); err == nil {
		t.Fatal("replacement inherited released target")
	}
	must(s.ReassignTarget(ctx, "project", "target", "node", 2))
	agent, generation, err := s.ReadHumanTargetAgent(ctx, admin, "project", "target")
	if err != nil || agent != "node" || generation != 2 {
		t.Fatal(agent, generation, err)
	}
}
