package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"

	"testing"
	"time"

	"github.com/maxtanforge/moori/internal/core/nodeops"
	"github.com/maxtanforge/moori/internal/node/ledger"
	"github.com/maxtanforge/moori/internal/transport/agentwire"
)

func TestMutationReceiptsSurviveRestartUntilAcknowledged(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "agent.db")
	book, err := ledger.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	c := Client{Session: Session{Ledger: book}, NodeOperation: func(context.Context, nodeops.Request) (nodeops.Result, error) {
		return nodeops.Result{Log: "transient log", Image: "busybox"}, nil
	}}
	w := nodeOperationWorker{}
	var active workerState
	if err := c.startNodeOperation(ctx, &active, &w, validNodeOperationCommand()); err != nil {
		t.Fatal(err)
	}
	w.wg.Wait()
	if err := c.startNodeOperation(ctx, &active, &w, validNodeOperationCommand()); err != nil {
		t.Fatal(err)
	}
	w.wg.Wait()
	if len(w.results.items) != 1 {
		t.Fatal("mutation was repeated")
	}
	pendingID := strings.Repeat("c", 32)
	if inserted, err := book.BeginNodeOperation(ctx, pendingID); err != nil || !inserted {
		t.Fatal(inserted, err)
	}
	if err := book.Close(); err != nil {
		t.Fatal(err)
	}
	book, err = ledger.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer book.Close()
	receipts, err := book.RecoverNodeOperations(ctx)
	if err != nil || len(receipts) != 2 || receipts[0].State != nodeops.Succeeded ||
		receipts[0].Result == nil || receipts[0].Result.Image != "busybox" || receipts[0].Result.Log != "" ||
		receipts[1].ID != pendingID || receipts[1].State != nodeops.Uncertain {
		t.Fatalf("recovered receipts: %+v, %v", receipts, err)
	}
	first := receipts[0]
	replay := nodeOperationWorker{pending: &completedNodeOperation{durable: true, mutation: true, report: agentwire.NodeOperationResult{
		ID: first.ID, State: first.State, Result: first.Result,
	}}}
	if err := replay.flush(ctx, &scriptChannel{}, nil, book); err == nil {
		t.Fatal("missing ACK succeeded")
	}
	ack, err := agentwire.Encode(agentwire.KindNodeOperationResultAck, agentwire.Ack{ID: first.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := replay.flush(ctx, &scriptChannel{replies: []agentwire.Message{ack}}, nil, book); err != nil {
		t.Fatal(err)
	}
	receipts, err = book.RecoverNodeOperations(ctx)
	if err != nil || len(receipts) != 1 || receipts[0].ID != pendingID {
		t.Fatalf("after ACK: %+v, %v", receipts, err)
	}
}

func validNodeOperationCommand() agentwire.Message {
	command := agentwire.NodeOperation{
		ID: strings.Repeat("b", 32),
		// Inside the accepted start window: past the action timeout, within
		// timeout plus one minute of slack.
		ExpiresMS: time.Now().Add(nodeops.ContainerStart.Timeout() + 30*time.Second).UnixMilli(),
		Request:   nodeops.Request{Action: nodeops.ContainerStart, Target: strings.Repeat("a", 64), Generation: 1},
	}
	body, err := json.Marshal(command)
	if err != nil {
		panic(err)
	}
	return agentwire.Message{Kind: agentwire.KindNodeOperation, Body: body}
}

// A rejection reported while another result is still unflushed must queue
// behind it instead of blocking the session goroutine on its own mailbox:
// flush and wait are the only consumers, and they run on the same goroutine
// as the rejection itself.
func TestNodeOperationRejectionQueuesBehindUnflushedResult(t *testing.T) {
	ctx := t.Context()
	l, err := ledger.Open(ctx, t.TempDir()+"/ledger.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	client := Client{
		Session: Session{Ledger: l},
		NodeOperation: func(context.Context, nodeops.Request) (nodeops.Result, error) {
			return nodeops.Result{}, nil
		},
	}
	operations := nodeOperationWorker{results: nodeOperationQueue{ready: make(chan struct{}, 1)}}
	operations.results.offer(completedNodeOperation{report: agentwire.NodeOperationResult{ID: strings.Repeat("1", 32)}})

	active := &workerState{}
	if !active.tryBegin("another-operation", func() {}) {
		t.Fatal("fresh worker state rejected the reservation")
	}
	if err := client.startNodeOperation(ctx, active, &operations, validNodeOperationCommand()); err != nil {
		t.Fatal(err)
	}

	first, ok := operations.results.take()
	if !ok || first.report.ID != strings.Repeat("1", 32) {
		t.Fatalf("first queued result = %+v, ok = %v", first, ok)
	}
	second, ok := operations.results.take()
	if !ok || second.report.ID != strings.Repeat("b", 32) || second.report.State != nodeops.Failed {
		t.Fatalf("second queued result = %+v, ok = %v", second, ok)
	}
	if _, ok := operations.results.take(); ok {
		t.Fatal("queue kept results after draining both reports")
	}
}

// A ledger failure is not evidence of a pending deployment: the operation must
// surface the error so the session reconnects, instead of rejecting the
// command with a bogus reason.
func TestNodeOperationLedgerFailurePropagatesWithoutReport(t *testing.T) {
	ctx := t.Context()
	l, err := ledger.Open(ctx, t.TempDir()+"/ledger.db")
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	client := Client{
		Session:       Session{Ledger: l},
		NodeOperation: func(context.Context, nodeops.Request) (nodeops.Result, error) { return nodeops.Result{}, nil },
	}
	operations := nodeOperationWorker{results: nodeOperationQueue{ready: make(chan struct{}, 1)}}

	err = client.startNodeOperation(ctx, &workerState{}, &operations, validNodeOperationCommand())
	if err == nil || errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("closed-ledger failure = %v, want a propagated ledger error", err)
	}
	if _, ok := operations.results.take(); ok {
		t.Fatal("ledger failure still reported a rejection")
	}
}

// wait must return as soon as any report arrives, well before the polling
// interval, and drain the queue in order across calls. An idle interval still
// ends the wait with no report.
func TestNodeOperationWaitWakesOnQueuedReport(t *testing.T) {
	operations := nodeOperationWorker{results: nodeOperationQueue{ready: make(chan struct{}, 1)}}
	if !operations.wait(t.Context(), time.Millisecond) {
		t.Fatal("wait reported context cancellation with a live context")
	}
	if operations.pending != nil {
		t.Fatalf("idle wait produced a report: %+v", operations.pending)
	}
	operations.results.offer(completedNodeOperation{report: agentwire.NodeOperationResult{ID: "first"}})
	operations.results.offer(completedNodeOperation{report: agentwire.NodeOperationResult{ID: "second"}})
	if !operations.wait(t.Context(), time.Hour) {
		t.Fatal("wait reported context cancellation with queued results")
	}
	if operations.pending == nil || operations.pending.report.ID != "first" {
		t.Fatalf("wait picked = %+v, want the first queued report", operations.pending)
	}
	operations.pending = nil
	if !operations.wait(t.Context(), time.Hour) {
		t.Fatal("wait reported context cancellation with a queued report")
	}
	if operations.pending == nil || operations.pending.report.ID != "second" {
		t.Fatalf("wait picked = %+v, want the second queued report", operations.pending)
	}
}
