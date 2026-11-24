package session

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/maxtanforge/moori/internal/node/localinv"
	"github.com/maxtanforge/moori/internal/transport/agentwire"
)

const inventoryRefresh = 30 * time.Second

// Collection runs independently so inspecting Docker cannot hold up execution
// polling or cancellation. Each connection starts with the latest result.
type machineInventory struct {
	latest  atomic.Pointer[agentwire.Message]
	refresh chan struct{}
	cancel  context.CancelFunc
	done    chan struct{}
}

func (c Client) collectMachine(ctx context.Context) *machineInventory {
	if c.Inventory == nil {
		return nil
	}
	ctx, cancel := context.WithCancel(ctx)
	i := &machineInventory{refresh: make(chan struct{}, 1), cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(i.done)
		for ctx.Err() == nil {
			view := c.Inventory(ctx)
			body, err := json.Marshal(view)
			if err != nil || len(body) > localinv.MaxSnapshotBytes {
				slog.Warn("machine inventory exceeds the wire budget or is invalid", "bytes", len(body), "err", err)
				body, _ = json.Marshal(localinv.Overview{GeneratedAt: time.Now().UTC(), Truncated: true, Message: "节点资源快照无法上报。"})
			}
			msg, err := agentwire.Encode(agentwire.KindNodeInventory, agentwire.NodeInventory{Overview: body})
			if err == nil {
				i.latest.Store(&msg)
			}
			timer := time.NewTimer(inventoryRefresh)
			select {
			case <-ctx.Done():
			case <-timer.C:
			case <-i.refresh:
			case <-c.InventoryRefresh:
			}
			timer.Stop()
		}
	}()
	return i
}

func (i *machineInventory) stop() {
	if i != nil {
		i.cancel()
		<-i.done
	}
}

func (i *machineInventory) resample() {
	if i != nil {
		select {
		case i.refresh <- struct{}{}:
		default:
		}
	}
}

func (i *machineInventory) send(ch messageChannel, last **agentwire.Message) error {
	if i == nil {
		return nil
	}
	msg := i.latest.Load()
	if msg == nil || msg == *last {
		return nil
	}
	reply, err := exchange(ch, *msg)
	if err != nil {
		return err
	}
	if err := agentwire.Decode(reply, agentwire.KindNodeInventoryAck, &struct{}{}); err != nil {
		return err
	}
	*last = msg
	return nil
}
