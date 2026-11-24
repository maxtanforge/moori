package dispatch_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/maxtanforge/moori/internal/control/dispatch"
	"github.com/maxtanforge/moori/internal/control/observability"
	"github.com/maxtanforge/moori/internal/control/storage"
	"github.com/maxtanforge/moori/internal/transport/agentwire"
)

func TestNodeInventoryStoredAndAcknowledged(t *testing.T) {
	ctx := context.Background()
	s, err := storage.Open(ctx, filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	health, err := observability.Open(ctx, filepath.Join(t.TempDir(), "metrics.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer health.Close()
	sess := dispatch.Session{Store: s, Health: health}
	peer := agentwire.Peer{ID: "agent-1", Generation: 3}
	report, err := agentwire.Encode(agentwire.KindNodeInventory, agentwire.NodeInventory{Overview: json.RawMessage(`{"available":true,"container_count":2,"running_count":2}`)})
	if err != nil {
		t.Fatal(err)
	}
	reply, err := sess.Reply(ctx, peer, report)
	if err != nil {
		t.Fatal(err)
	}
	var ack struct{}
	if err := agentwire.Decode(reply, agentwire.KindNodeInventoryAck, &ack); err != nil {
		t.Fatal(err)
	}
	record, err := health.AgentInventory(ctx, "agent-1")
	if err != nil || record.Generation != 3 || record.UpdatedAt == nil {
		t.Fatalf("inventory not stored: %+v %v", record, err)
	}
	var overview struct {
		Available      bool `json:"available"`
		ContainerCount int  `json:"container_count"`
	}
	if err := json.Unmarshal(record.Overview, &overview); err != nil || !overview.Available || overview.ContainerCount != 2 {
		t.Fatalf("stored overview: %s %v", record.Overview, err)
	}
	// A frame without an overview (protocol drift) is acknowledged but stores
	// nothing. Structurally invalid JSON cannot reach this handler at all:
	// agentwire.Decode rejects it before the store sees the message.
	empty := agentwire.Message{Kind: agentwire.KindNodeInventory, Body: []byte(`{}`)}
	if reply, err := sess.Reply(ctx, peer, empty); err != nil {
		t.Fatal(err)
	} else if err := agentwire.Decode(reply, agentwire.KindNodeInventoryAck, &struct{}{}); err != nil {
		t.Fatal(err)
	}
	if kept, _ := health.AgentInventory(ctx, "agent-1"); string(kept.Overview) != string(record.Overview) {
		t.Fatal("malformed snapshot replaced the stored one")
	}
	// Without a health store the exchange still acknowledges.
	bare := dispatch.Session{Store: s}
	if _, err := bare.Reply(ctx, peer, report); err != nil {
		t.Fatal(err)
	}
}
