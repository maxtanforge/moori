package localinv

import (
	"context"
	"time"

	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/api/types/filters"
)

type eventSource interface {
	Events(context.Context, events.ListOptions) (<-chan events.Message, <-chan error)
}

// WatchChanges wakes the existing inventory collector for daemon changes made
// by any client. A fixed window batches Compose bursts without waiting for a
// busy daemon to go quiet. The regular inventory timer covers missed events.
func (i *Inventory) WatchChanges(ctx context.Context, refresh chan<- struct{}) {
	source, ok := i.docker.(eventSource)
	if !ok {
		return
	}
	options := events.ListOptions{Filters: filters.NewArgs(
		filters.Arg("type", "container"), filters.Arg("type", "image"),
		filters.Arg("type", "network"), filters.Arg("type", "volume"),
	)}
	for reconnect := false; ctx.Err() == nil; reconnect = true {
		messages, failures := source.Events(ctx, options)
		watchInventoryStream(ctx, messages, failures, refresh, reconnect)
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}

func watchInventoryStream(ctx context.Context, messages <-chan events.Message, failures <-chan error, refresh chan<- struct{}, reconnect bool) {
	var ready <-chan time.Time
	if reconnect {
		// Reconcile events missed during the gap, even if the new stream is quiet.
		// A failed subscription exits before this window and does not trigger a scan.
		ready = time.After(2 * time.Second)
	}
	var dirty bool
	notify := func() {
		select {
		case refresh <- struct{}{}:
		default:
		}
	}
	defer func() {
		if dirty && ctx.Err() == nil {
			notify()
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case <-failures:
			return
		case event, ok := <-messages:
			if !ok {
				return
			}
			if changesInventory(event) {
				dirty = true
				if ready == nil {
					ready = time.After(2 * time.Second)
				}
			}
		case <-ready:
			dirty = false
			ready = nil
			notify()
		}
	}
}

func changesInventory(m events.Message) bool {
	switch m.Type {
	case events.ContainerEventType:
		switch m.Action {
		case events.ActionCreate, events.ActionStart, events.ActionRestart, events.ActionStop,
			events.ActionDie, events.ActionDestroy, events.ActionRename, events.ActionUpdate,
			events.ActionPause, events.ActionUnPause, events.ActionHealthStatusHealthy,
			events.ActionHealthStatusRunning, events.ActionHealthStatusUnhealthy:
			return true
		}
	case events.ImageEventType:
		switch m.Action {
		case events.ActionCreate, events.ActionPull, events.ActionTag, events.ActionUnTag, events.ActionDelete, events.ActionLoad, events.ActionImport, events.ActionPrune:
			return true
		}
	case events.NetworkEventType, events.VolumeEventType:
		return true
	}
	return false
}
