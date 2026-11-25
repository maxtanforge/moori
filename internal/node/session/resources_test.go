package session

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/maxtanforge/moori/internal/node/localinv"
	"github.com/maxtanforge/moori/internal/transport/agentwire"
)

func TestMachineInventoryPublishesHostChangeBeforePeriodicSample(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var containerCount int
		change := make(chan struct{})
		c := Client{
			Inventory: func(context.Context) localinv.Overview {
				return localinv.Overview{Available: true, ContainerCount: containerCount}
			},
			WatchInventory: func(ctx context.Context, refresh chan<- struct{}) {
				select {
				case <-change:
					refresh <- struct{}{}
				case <-ctx.Done():
					return
				}
				<-ctx.Done()
			},
		}
		machine := c.collectMachine(t.Context())
		defer machine.stop()
		synctest.Wait()
		var last *agentwire.Message
		ack, err := agentwire.Encode(agentwire.KindNodeInventoryAck, struct{}{})
		if err != nil {
			t.Fatal(err)
		}
		ch := &scriptChannel{replies: []agentwire.Message{ack, ack}}
		if err := machine.send(ch, &last); err != nil {
			t.Fatal(err)
		}
		containerCount = 1
		close(change)
		synctest.Wait()
		if err := machine.send(ch, &last); err != nil || len(ch.sent) != 2 {
			t.Fatalf("new inventory not sent: %v", err)
		}
		var report agentwire.NodeInventory
		var view localinv.Overview
		if agentwire.Decode(ch.sent[1], agentwire.KindNodeInventory, &report) != nil || json.Unmarshal(report.Overview, &view) != nil || view.ContainerCount != 1 {
			t.Fatal("old inventory after host event")
		}
	})
}

type scriptChannel struct {
	replies []agentwire.Message
	sent    []agentwire.Message
}

func (c *scriptChannel) Send(m agentwire.Message) error {
	c.sent = append(c.sent, m)
	return nil
}

func (c *scriptChannel) Receive() (agentwire.Message, error) {
	if len(c.replies) == 0 {
		return agentwire.Message{}, errors.New("no scripted reply")
	}
	m := c.replies[0]
	c.replies = c.replies[1:]
	return m, nil
}

func (c *scriptChannel) Close() error { return nil }

func TestMachineInventoryDoesNotBlockPollingAndResendsOnReconnect(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started, release := make(chan struct{}), make(chan struct{})
	client := Client{Inventory: func(ctx context.Context) localinv.Overview {
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
		}
		return localinv.Overview{Available: true, ContainerCount: 2}
	}}
	sampler := client.collectMachine(ctx)
	defer sampler.stop()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("collection did not start")
	}
	ack, err := agentwire.Encode(agentwire.KindNodeInventoryAck, struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	ch := &scriptChannel{replies: []agentwire.Message{ack, ack}}
	var last *agentwire.Message
	if err := sampler.send(ch, &last); err != nil || len(ch.sent) != 0 {
		t.Fatal(err, len(ch.sent))
	}
	close(release)
	for deadline := time.Now().Add(time.Second); len(ch.sent) == 0 && time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		if err := sampler.send(ch, &last); err != nil {
			t.Fatal(err)
		}
	}
	if err := sampler.send(ch, &last); err != nil || len(ch.sent) != 1 {
		t.Fatal(err, len(ch.sent))
	}
	var report agentwire.NodeInventory
	if err := agentwire.Decode(ch.sent[0], agentwire.KindNodeInventory, &report); err != nil {
		t.Fatal(err)
	}
	var overview localinv.Overview
	if err := json.Unmarshal(report.Overview, &overview); err != nil || overview.ContainerCount != 2 {
		t.Fatal(err, overview)
	}
	last = nil
	if err := sampler.send(ch, &last); err != nil || len(ch.sent) != 2 {
		t.Fatal(err, len(ch.sent))
	}
}

func TestInventoryRetrySendsLatestWithoutHoldingPollLoop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		machine := new(machineInventory)
		first := agentwire.Message{Kind: agentwire.KindNodeInventory}
		next := agentwire.Message{Kind: agentwire.KindNodeInventory, Body: []byte("{}")}
		machine.latest.Store(&first)
		retry := agentwire.Message{Kind: agentwire.KindNodeInventoryAck, Body: []byte(`{"retry":true}`)}
		ack := agentwire.Message{Kind: agentwire.KindNodeInventoryAck, Body: []byte("{}")}
		ch := &scriptChannel{replies: []agentwire.Message{retry, retry, ack}}
		var last *agentwire.Message
		send := func(want int) {
			t.Helper()
			before := time.Now()
			if err := machine.send(ch, &last); err != nil || len(ch.sent) != want || time.Now() != before {
				t.Fatalf("send count %d, want %d, elapsed %s: %v", len(ch.sent), want, time.Since(before), err)
			}
		}
		send(1)
		machine.latest.Store(&next)
		send(1)
		time.Sleep(time.Second)
		send(2)
		if string(ch.sent[1].Body) != "{}" || last != nil {
			t.Fatal("expected retry of latest unacknowledged snapshot")
		}
		time.Sleep(time.Second)
		send(2)
		time.Sleep(time.Second)
		send(3)
		send(3)
		if last != &next {
			t.Fatal("missing receipt for latest snapshot")
		}
	})
}

func TestEventScansDoNotDelayPeriodicFullScan(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var full, fast int
		c := Client{
			Inventory:      func(context.Context) localinv.Overview { full++; return localinv.Overview{} },
			InventoryLists: func(context.Context) localinv.Overview { fast++; return localinv.Overview{} },
		}
		machine := c.collectMachine(t.Context())
		defer machine.stop()
		synctest.Wait()
		for range 5 {
			time.Sleep(inventoryRefresh / 5)
			machine.resample()
			synctest.Wait()
		}
		if full != 2 || fast < 4 {
			t.Fatalf("after one interval got %d full scans and %d list scans", full, fast)
		}
	})
}
