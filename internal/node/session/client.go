package session

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/maxtanforge/moori/internal/core/nodeops"
	"github.com/maxtanforge/moori/internal/core/telemetry"
	"github.com/maxtanforge/moori/internal/node/localinv"
	"github.com/maxtanforge/moori/internal/platform/execlog"
	"github.com/maxtanforge/moori/internal/transport/agentwire"
)

type Client struct {
	outputAfter *time.Time
	Session     Session
	Dial        func(context.Context) (*agentwire.Channel, error)
	// Direct serves the built-in executor in process, without an Agent connection.
	Direct   func(context.Context, agentwire.Message) (agentwire.Message, error)
	Execute  func(context.Context, string, string) error
	Log      *slog.Logger
	Interval time.Duration
	Logs     *execlog.Store
	Collect  func() (telemetry.Sample, error)
	// Inventory reports this machine's Docker resources and filesystem
	// usage; the first report goes out right after connecting.
	Inventory        func(context.Context) localinv.Overview
	InventoryRefresh <-chan struct{}
	NodeOperation    func(context.Context, nodeops.Request) (nodeops.Result, error)
}

// Run keeps one execution worker across channel reconnects. Only Agent
// shutdown, rather than loss of the connection, cancels the worker.
func (c Client) Run(ctx context.Context) error {
	if c.Session.Ledger == nil || (c.Dial == nil && c.Direct == nil) || c.Execute == nil {
		return errors.New("Agent client is not configured")
	}
	if c.Log == nil {
		c.Log = slog.Default()
	}
	c.outputAfter = new(time.Time)
	if c.Interval <= 0 || c.Interval > 10*time.Second {
		c.Interval = time.Second
	}
	if err := c.Session.Ledger.RecoverInterrupted(ctx); err != nil {
		return err
	}
	machine := c.collectMachine(ctx)
	defer machine.stop()
	var active workerState
	operations := nodeOperationWorker{results: nodeOperationQueue{ready: make(chan struct{}, 1)}}
	recovered, err := c.Session.Ledger.RecoverNodeOperations(ctx)
	if err != nil {
		return err
	}
	for _, receipt := range recovered {
		operations.results.offer(completedNodeOperation{durable: true, mutation: true, report: agentwire.NodeOperationResult{
			ID: receipt.ID, State: receipt.State, Error: receipt.Error, Result: receipt.Result,
		}})
	}
	defer operations.wg.Wait()
	var worker sync.WaitGroup
	wake := make(chan struct{}, 1)
	worker.Go(func() { c.execute(ctx, &active, wake) })
	defer worker.Wait()
	reconnect := c.Interval
	wasConnected := false
	for ctx.Err() == nil {
		var ch messageChannel
		var err error
		if c.Direct != nil {
			ch = newDirectChannel(ctx, c.Direct)
		} else {
			ch, err = c.Dial(ctx)
		}
		if err == nil {
			reconnect = c.Interval
			if wasConnected {
				machine.resample()
			}
			wasConnected = true
			err = c.connected(ctx, ch, &active, wake, machine, &operations)
		}
		if ctx.Err() != nil {
			break
		}
		if err != nil {
			c.Log.Debug("Agent channel disconnected", "err", err)
		}
		if !delay(ctx, reconnect) {
			break
		}
		reconnect = min(reconnect*2, 10*time.Second)
	}
	return ctx.Err()
}

func (c Client) execute(ctx context.Context, active *workerState, wake <-chan struct{}) {
	ticker := time.NewTicker(c.Interval)
	defer ticker.Stop()
	for ctx.Err() == nil {
		entry, err := c.Session.Ledger.NextAccepted(ctx)
		if err == nil {
			runCtx, cancel := context.WithCancel(ctx)
			if active.tryBegin(entry.Execution, cancel) {
				err = c.Execute(runCtx, entry.Execution, entry.Document)
				active.end()
			}
			cancel()
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) && ctx.Err() == nil {
			c.Log.Warn("Agent execution needs attention", "execution", entry.Execution, "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-wake:
		}
	}
}

func (c Client) connected(ctx context.Context, ch messageChannel, active *workerState, wake chan<- struct{}, machine *machineInventory, operations *nodeOperationWorker) error {
	defer ch.Close()
	stop := context.AfterFunc(ctx, func() { ch.Close() })
	defer stop()
	var progress inventoryProgress
	var nextSample time.Time
	var latest *agentwire.Message
	for ctx.Err() == nil {
		if err := operations.flush(ctx, ch, machine, c.Session.Ledger); err != nil {
			return err
		}
		if err := c.cycle(ctx, ch, active, wake, &progress, &nextSample, operations); err != nil {
			return err
		}
		if err := machine.send(ch, &latest); err != nil {
			return err
		}
		if !operations.wait(ctx, c.Interval) {
			break
		}
	}
	return ctx.Err()
}

func (c Client) cycle(ctx context.Context, ch messageChannel, active *workerState, wake chan<- struct{}, progress *inventoryProgress, nextSample *time.Time, operations *nodeOperationWorker) error {
	const batchSize = 32
	pending, err := c.Session.Ledger.Pending(ctx, batchSize)
	if err != nil {
		return err
	}
	acks := make([]agentwire.Message, 0, len(pending))
	for _, event := range pending {
		m, err := Report(event)
		if err != nil {
			return err
		}
		ack, err := exchange(ch, m)
		if err != nil {
			return err
		}
		acks = append(acks, ack)
	}
	if err := c.Session.AcknowledgeAll(ctx, pending, acks); err != nil {
		return err
	}
	if len(pending) == batchSize {
		return nil
	}
	busy := active.busy()
	kind := operations.kind(busy, c.NodeOperation != nil, time.Now())
	m, err := agentwire.Encode(kind, struct{}{})
	if err != nil {
		return err
	}
	reply, err := exchange(ch, m)
	if err != nil {
		// A control server predating this capability rejects the new kind.
		// Keep deployment traffic working while periodically retrying it.
		if kind == agentwire.KindNodeOperationPoll || kind == agentwire.KindNodeOperationHeartbeat {
			operations.probeAfter = time.Now().Add(2 * time.Minute)
		}
		return err
	}
	if reply.Kind == agentwire.KindIdle {
		if err := agentwire.Decode(reply, agentwire.KindIdle, &struct{}{}); err != nil {
			return err
		}
		return c.diagnostics(ch, nextSample)
	}
	if reply.Kind == agentwire.KindRecovery {
		if err := c.sendLedgerInventory(ctx, ch, reply, progress); err != nil {
			return err
		}
		if progress.complete {
			return c.diagnostics(ch, nextSample)
		}
		return nil
	}
	if reply.Kind == agentwire.KindCancel {
		return c.cancelExecution(ctx, ch, active, reply)
	}
	if reply.Kind == agentwire.KindNodeOperation && kind == agentwire.KindNodeOperationPoll {
		return c.startNodeOperation(ctx, active, operations, reply)
	}
	if busy {
		return errors.New("unexpected heartbeat response")
	}
	admitted, err := c.Session.Accept(ctx, reply, time.Now())
	if err != nil {
		return err
	}
	// Execution is owned by the local ledger, even if receipt delivery fails.
	select {
	case wake <- struct{}{}:
	default:
	}
	response, err := exchange(ch, admitted.Receipt)
	if err != nil {
		return err
	}
	var ack agentwire.Ack
	if err := agentwire.Decode(response, agentwire.KindReceiptAck, &ack); err != nil {
		return err
	}
	if ack.ID != admitted.Execution {
		return errors.New("receipt acknowledgement does not match execution")
	}
	return nil
}

func exchange(ch messageChannel, m agentwire.Message) (agentwire.Message, error) {
	if err := ch.Send(m); err != nil {
		return agentwire.Message{}, err
	}
	return ch.Receive()
}

func delay(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
