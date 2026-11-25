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
	// The single channel loop owns retry timing across connection attempts.
	retryAfter time.Time
	retryDelay time.Duration
	latest     atomic.Pointer[agentwire.Message]
	refresh    chan struct{}
	cancel     context.CancelFunc
	done       chan struct{}
}

func (c Client) collectMachine(ctx context.Context) *machineInventory {
	if c.Inventory == nil {
		return nil
	}
	ctx, cancel := context.WithCancel(ctx)
	i := &machineInventory{refresh: make(chan struct{}, 1), cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(i.done)
		if c.WatchInventory != nil {
			stopped := make(chan struct{})
			go func() {
				defer close(stopped)
				c.WatchInventory(ctx, i.refresh)
			}()
			defer func() { <-stopped }()
		}
		ticker := time.NewTicker(inventoryRefresh)
		defer ticker.Stop()
		collect := c.Inventory
		for ctx.Err() == nil {
			view := collect(ctx)
			body, err := json.Marshal(view)
			if err != nil || len(body) > localinv.MaxSnapshotBytes {
				slog.Warn("machine inventory exceeds the wire budget or is invalid", "bytes", len(body), "err", err)
				body, _ = json.Marshal(localinv.Overview{GeneratedAt: time.Now().UTC(), Truncated: true, Message: "节点资源快照无法上报。"})
			}
			msg, err := agentwire.Encode(agentwire.KindNodeInventory, agentwire.NodeInventory{Overview: body})
			if err == nil {
				i.latest.Store(&msg)
			}
			collect = c.Inventory
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				continue
			case <-i.refresh:
			case <-c.InventoryRefresh:
			}
			// Give an already-due periodic scan priority over event traffic.
			select {
			case <-ticker.C:
			default:
				if c.InventoryLists != nil {
					collect = c.InventoryLists
				}
			}
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
	if msg == nil || msg == *last || time.Now().Before(i.retryAfter) {
		return nil
	}
	reply, err := exchange(ch, *msg)
	if err != nil {
		return err
	}
	var ack agentwire.NodeInventoryAck
	if err := agentwire.Decode(reply, agentwire.KindNodeInventoryAck, &ack); err != nil {
		return err
	}
	if ack.Retry {
		i.retryDelay = min(max(time.Second, 2*i.retryDelay), 30*time.Second)
		i.retryAfter = time.Now().Add(i.retryDelay)
		return nil
	}
	i.retryDelay = 0
	i.retryAfter = time.Time{}
	if ack.Rejected {
		slog.Warn("control rejected the machine inventory")
	}
	*last = msg
	return nil
}
