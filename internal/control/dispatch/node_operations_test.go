package dispatch_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maxtanforge/moori/internal/control/dispatch"
	"github.com/maxtanforge/moori/internal/control/storage"
	"github.com/maxtanforge/moori/internal/core/nodeops"
	"github.com/maxtanforge/moori/internal/transport/agentwire"
)

func TestNodeOperationResultWithoutRecordDoesNotBlockPolling(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.EnsureLocalExecutor(ctx); err != nil {
		t.Fatal(err)
	}
	session := dispatch.Session{Store: store, ValidatePeer: func(ctx context.Context, peer agentwire.Peer) error {
		return store.ValidateAgent(ctx, peer.ID, peer.Generation)
	}}
	peer := agentwire.Peer{ID: storage.LocalExecutorID, Generation: 1}
	id := strings.Repeat("a", 32)
	report, err := agentwire.Encode(agentwire.KindNodeOperationResult, agentwire.NodeOperationResult{
		ID: id, State: nodeops.Succeeded, Result: &nodeops.Result{},
	})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		reply, err := session.Reply(ctx, peer, report)
		if err != nil {
			t.Fatal(err)
		}
		var ack agentwire.Ack
		if err := agentwire.Decode(reply, agentwire.KindNodeOperationResultAck, &ack); err != nil || ack.ID != id {
			t.Fatalf("ack = %+v, err = %v", ack, err)
		}
	}
	poll, err := agentwire.Encode(agentwire.KindNodeOperationPoll, struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	reply, err := session.Reply(ctx, peer, poll)
	if err != nil || reply.Kind != agentwire.KindIdle {
		t.Fatalf("poll returned %q, err = %v", reply.Kind, err)
	}
}
