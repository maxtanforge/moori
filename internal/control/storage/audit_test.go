package storage

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
)

func TestAuditRecordsDeploymentAndCancellationInOrder(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "control.db")
	s := openTestStore(t, path)
	_, err := s.db.ExecContext(ctx, `INSERT INTO targets(id,project) VALUES ('target','project');
 INSERT INTO deployments(id,project,caller,request_key,target,release_id,digest,state)
 VALUES ('deployment','project','requester','key','target','release','digest','canceled');
 INSERT INTO deployment_audit(deployment_id,state,created_at) VALUES
 ('deployment','queued','2026-01-01 00:00:01'),('deployment','dispatching','2026-01-01 00:00:02');
 INSERT INTO deployment_cancellations(deployment_id,operator,note,requested_at)
 VALUES ('deployment','operator','requested','2026-01-01 00:00:03');
 INSERT INTO deployment_audit(deployment_id,state,created_at) VALUES ('deployment','canceled','2026-01-01 00:00:03');`)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT action,detail FROM project_audit ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var events []string
	for rows.Next() {
		var action, detail string
		if err := rows.Scan(&action, &detail); err != nil {
			t.Fatal(err)
		}
		events = append(events, action+"/"+detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := []string{"target.registered/", "deployment.requested/queued", "deployment.state_changed/dispatching",
		"deployment.cancellation_requested/requested", "deployment.state_changed/canceled"}
	if !slices.Equal(events, want) {
		t.Fatalf("audit event order: %v", events)
	}
}
