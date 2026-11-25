package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestMonitoringEventsShowActualTimesAndCurrentGeneration(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC().Truncate(time.Second)
	run := func(query string, params ...any) {
		t.Helper()
		if _, err := store.db.ExecContext(ctx, query, params...); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.RegisterTarget(ctx, "app", "prod"); err != nil {
		t.Fatal(err)
	}
	run(`INSERT INTO deployments(id,project,caller,request_key,target,release_id,digest,state,agent,generation)
	VALUES ('normal','app','tester','one','prod','r1','hash','succeeded','worker',2),
	       ('old','app','tester','two','prod','r0','hash','succeeded','worker',1)`)
	run(`INSERT INTO deployments(id,project,caller,request_key,target,release_id,digest,state,agent,generation,rollback_of)
	VALUES ('rollback','app','tester','three','prod','r0','hash','succeeded','worker',2,'normal')`)
	const clockFormat = "2006-01-02 15:04:05"
	run(`INSERT INTO deployment_audit(deployment_id,state,created_at) VALUES ('normal','executing',?),('rollback','succeeded',?),('old','succeeded',?)`,
		now.Add(-30*time.Minute).Format(clockFormat), now.Add(-15*time.Minute).Format(clockFormat), now.Add(-3*time.Minute).Format(clockFormat))
	restartAt := now.Add(-time.Minute)
	run(`INSERT INTO node_operations(id,agent,generation,actor,action,request,mutation,state,created_ms,expires_ms,completed_ms)
	VALUES ('op','worker',2,'admin','container.restart',?,1,'succeeded',?,?,?)`,
		`{"target":"abcdef1234567890"}`, now.Add(-4*time.Minute).UnixMilli(), now.UnixMilli(), restartAt.UnixMilli())
	items, err := store.MonitoringEvents(ctx, "worker", 2, now.Add(-time.Hour), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 || items[0].Kind != "container_restart" || !items[0].At.Equal(restartAt) ||
		items[1].Kind != "rollback_succeeded" || items[2].Kind != "deployment_started" {
		t.Fatalf("unexpected events: %+v", items)
	}
}
