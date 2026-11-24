package observability

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

func TestAgentInventoryKeepsLatestSnapshot(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "metrics.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if record, err := s.AgentInventory(ctx, "agent-1"); err != nil || record.Agent != "agent-1" || len(record.Overview) != 0 {
		t.Fatalf("unknown agent: %+v %v", record, err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	overview := json.RawMessage(`{"available":true,"container_count":3}`)
	if err := s.RecordInventory(ctx, "agent-1", 1, overview, now); err != nil {
		t.Fatal(err)
	}
	record, err := s.AgentInventory(ctx, "agent-1")
	if err != nil || record.Generation != 1 || record.UpdatedAt == nil || !record.UpdatedAt.Equal(now) || string(record.Overview) != string(overview) {
		t.Fatalf("stored inventory: %+v %v", record, err)
	}
	// A change immediately after a Docker operation can arrive within seconds.
	changed := json.RawMessage(`{"available":true,"container_count":9}`)
	if err := s.RecordInventory(ctx, "agent-1", 1, changed, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordInventory(ctx, "agent-1", 1, overview, now); err != nil {
		t.Fatal(err)
	}
	if record, _ := s.AgentInventory(ctx, "agent-1"); string(record.Overview) != string(changed) {
		t.Fatal("fresh snapshot was not retained")
	}
	// A newer generation always wins (Agent restarted).
	next := json.RawMessage(`{"available":true,"container_count":5}`)
	if err := s.RecordInventory(ctx, "agent-1", 2, next, now); err != nil {
		t.Fatal(err)
	}
	if record, _ := s.AgentInventory(ctx, "agent-1"); record.Generation != 2 || string(record.Overview) != string(next) {
		t.Fatal("newer generation did not win:", record)
	}
	if err := s.RecordInventory(ctx, "agent-1", 2, json.RawMessage(`{broken`), now.Add(time.Minute)); err == nil {
		t.Fatal("malformed overview accepted")
	}
	if err := s.RecordInventory(ctx, "", 2, next, now.Add(time.Minute)); err == nil {
		t.Fatal("invalid peer accepted")
	}
}
