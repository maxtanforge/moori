package storage

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maxtanforge/moori/internal/core/nodeops"
)

func TestContainerEditResultIsTemporaryAndPrivateToRequester(t *testing.T) {
	ctx := t.Context()
	s := openTestStore(t, filepath.Join(t.TempDir(), "control.db"))
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	token, err := s.InitializeAdmin(ctx, "admin", "initial password", time.Hour)
	must(err)
	_, err = s.CreateHumanUser(ctx, "second", "another password")
	must(err)
	must(s.SetPlatformAdmin(ctx, "second", true))
	other, err := s.LoginHuman(ctx, "second", "another password", time.Hour)
	must(err)
	must(s.BindAgent(ctx, "node-1", 1, [32]byte{1}))
	now := time.Now()
	must(s.RecordNodeOperationCapability(ctx, "node-1", 1, now))
	req := nodeops.Request{Action: nodeops.ContainerEditConfig, Target: strings.Repeat("a", 64)}
	op, err := s.QueueNodeOperation(ctx, token, "node-1", 1, req, now)
	must(err)
	_, _, err = s.NextNodeOperation(ctx, "node-1", 1, now)
	must(err)
	value := "application value\nwith a newline"
	result := &nodeops.Result{EditConfig: &nodeops.ContainerEditConfigResult{
		Environment: map[string]*string{"APP_TEXT": &value},
		Labels:      map[string]string{"app.owner": "team"},
	}}
	must(s.CompleteNodeOperation(ctx, "node-1", 1, op.ID, nodeops.Succeeded, "", result, now))
	var saved []byte
	must(s.db.QueryRowContext(ctx, "SELECT result FROM node_operations WHERE id=?", op.ID).Scan(&saved))
	if saved != nil {
		t.Fatal("editor values persisted to SQLite")
	}
	if _, err := s.GetNodeOperation(ctx, other, "node-1", op.ID, now); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("another administrator could read this result")
	}
	got, err := s.GetNodeOperation(ctx, token, "node-1", op.ID, now)
	must(err)
	if got.Result == nil || got.Result.EditConfig == nil || got.Result.EditConfig.Environment["APP_TEXT"] == nil ||
		*got.Result.EditConfig.Environment["APP_TEXT"] != value || got.Result.EditConfig.Labels["app.owner"] != "team" {
		t.Fatal("editor did not receive the original configuration")
	}
	got, err = s.GetNodeOperation(ctx, token, "node-1", op.ID, now.Add(time.Minute))
	must(err)
	if got.Result != nil || got.State != nodeops.Failed {
		t.Fatal("editor configuration was available after expiration")
	}
}
