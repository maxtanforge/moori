package session

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/maxtanforge/moori/internal/node/localinv"
	"github.com/maxtanforge/moori/internal/transport/agentwire"
)

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
