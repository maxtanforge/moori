package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"regexp"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/maxtanforge/moori/internal/core/nodeops"
	"github.com/maxtanforge/moori/internal/node/ledger"
	"github.com/maxtanforge/moori/internal/transport/agentwire"
)

var operationID = regexp.MustCompile("^[a-f0-9]{32}$")

type nodeOperationWorker struct {
	wg         sync.WaitGroup
	results    nodeOperationQueue
	pending    *completedNodeOperation
	probeAfter time.Time
}

type completedNodeOperation struct {
	report   agentwire.NodeOperationResult
	mutation bool
	durable  bool
}

// nodeOperationQueue buffers finished reports without ever blocking their
// producers. The bounded channel it replaced could deadlock: rejection paths
// report from the session goroutine whose flush/wait loop is the only
// consumer, so a second rejection while one result was still unflushed
// blocked that goroutine on its own mailbox forever.
type nodeOperationQueue struct {
	mu    sync.Mutex
	items []completedNodeOperation
	ready chan struct{}
}

func (q *nodeOperationQueue) offer(result completedNodeOperation) {
	q.mu.Lock()
	q.items = append(q.items, result)
	q.mu.Unlock()
	select {
	case q.ready <- struct{}{}:
	default:
	}
}

func (q *nodeOperationQueue) take() (completedNodeOperation, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) == 0 {
		return completedNodeOperation{}, false
	}
	result := q.items[0]
	q.items = q.items[1:]
	return result, true
}

func (w *workerState) tryBegin(id string, cancel context.CancelFunc) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.id != "" {
		return false
	}
	w.id, w.cancel = id, cancel
	return true
}

func (w *nodeOperationWorker) kind(busy bool, enabled bool, now time.Time) string {
	if enabled && !now.Before(w.probeAfter) {
		if busy {
			return agentwire.KindNodeOperationHeartbeat
		}
		return agentwire.KindNodeOperationPoll
	}
	if busy {
		return agentwire.KindHeartbeat
	}
	return agentwire.KindPoll
}

func (c Client) startNodeOperation(ctx context.Context, active *workerState, operations *nodeOperationWorker, m agentwire.Message) error {
	var command agentwire.NodeOperation
	if err := agentwire.Decode(m, agentwire.KindNodeOperation, &command); err != nil {
		return err
	}
	req := command.Request
	req.Operation = command.ID
	if c.NodeOperation == nil || !operationID.MatchString(req.Operation) || req.Generation <= 0 || req.Validate() != nil {
		return errors.New("invalid node operation")
	}
	report := agentwire.NodeOperationResult{ID: command.ID, State: nodeops.Failed}
	mutation := req.Action.Mutates()
	deadline := time.UnixMilli(command.ExpiresMS)
	now := time.Now()
	if deadline.Add(-req.Action.Timeout()).Before(now) || deadline.After(now.Add(req.Action.Timeout()+time.Minute)) {
		report.Error = "the node received the Docker operation after its start window"
		operations.results.offer(completedNodeOperation{report: report})
		return nil
	}
	switch _, err := c.Session.Ledger.NextAccepted(ctx); {
	case err == nil:
		report.Error = "the node has a pending deployment"
		operations.results.offer(completedNodeOperation{report: report})
		return nil
	case !errors.Is(err, sql.ErrNoRows):
		// A ledger failure is not evidence of a pending deployment; reconnect
		// and retry instead of rejecting the operation.
		return err
	}
	runCtx, cancel := context.WithTimeout(ctx, req.Action.Timeout())
	if !active.tryBegin("node-operation:"+command.ID, cancel) {
		cancel()
		report.Error = "the node is executing another operation"
		operations.results.offer(completedNodeOperation{report: report})
		return nil
	}
	if mutation {
		accepted, err := c.Session.Ledger.BeginNodeOperation(ctx, command.ID)
		if err != nil || !accepted {
			active.end()
			cancel()
			return err
		}
	}
	operations.wg.Go(func() {
		defer active.end()
		defer cancel()
		result, err := c.NodeOperation(runCtx, req)
		switch {
		case errors.Is(err, nodeops.ErrPullStalled):
			report.State, report.Error = nodeops.Uncertain, nodeops.ErrPullStalled.Error()
		case err != nil:
			report.Error = safeOperationError(err.Error())
			if mutation && (runCtx.Err() != nil || errors.Is(err, nodeops.ErrOutcomeUnknown) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
				report.State = nodeops.Uncertain
				report.Error = "Docker did not confirm the result. Refresh resources before retrying."
			}
		default:
			encoded, marshalErr := json.Marshal(result)
			if marshalErr != nil || len(encoded) > nodeops.MaxResultBytes || len(result.Log) > 3*nodeops.MaxLogBytes {
				report.Error = "the Docker response exceeded the operation output limit"
				if mutation {
					report.State = nodeops.Uncertain
				}
			} else {
				report.State, report.Result = nodeops.Succeeded, &result
			}
		}
		if mutation {
			saveCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer stop()
			if err := c.Session.Ledger.FinishNodeOperation(saveCtx, nodeops.Operation{
				ID: report.ID, State: report.State, Error: report.Error, Result: report.Result,
			}); err != nil {
				slog.Warn("could not persist Docker operation receipt", "operation", report.ID, "err", err)
			}
		}
		operations.results.offer(completedNodeOperation{report: report, mutation: mutation, durable: mutation})
	})
	return nil
}

// The polling interval limits idle checks. Completed operations can report
// sooner through the same ordered request/response channel.
func (w *nodeOperationWorker) wait(ctx context.Context, interval time.Duration) bool {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	for {
		if result, ok := w.results.take(); ok {
			w.pending = &result
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-timer.C:
			return true
		case <-w.results.ready:
		}
	}
}

func (w *nodeOperationWorker) flush(ctx context.Context, ch messageChannel, machine *machineInventory, l *ledger.Ledger) error {
	if w.pending == nil {
		result, ok := w.results.take()
		if !ok {
			return nil
		}
		w.pending = &result
	}
	m, err := agentwire.Encode(agentwire.KindNodeOperationResult, w.pending.report)
	if err != nil {
		return err
	}
	response, err := exchange(ch, m)
	if err != nil {
		return err
	}
	var ack agentwire.Ack
	if err := agentwire.Decode(response, agentwire.KindNodeOperationResultAck, &ack); err != nil {
		return err
	}
	if ack.ID != w.pending.report.ID {
		return errors.New("node operation acknowledgement does not match operation")
	}
	if w.pending.durable {
		if err := l.AckNodeOperation(ctx, ack.ID); err != nil {
			return err
		}
	}
	if w.pending.mutation {
		machine.resample()
	}
	w.pending = nil
	return nil
}

func safeOperationError(message string) string {
	if message == "" {
		return "the Docker operation failed"
	}
	message = message[:min(len(message), 500)]
	for !utf8.ValidString(message) && len(message) != 0 {
		message = message[:len(message)-1]
	}
	if message == "" {
		return "the Docker operation failed"
	}
	return message
}
