package localinv

import (
	"context"
	"io"
	"testing"
	"testing/synctest"
	"time"

	"github.com/docker/docker/api/types/events"
)

type eventDocker struct {
	*fakeDocker
	messages      chan events.Message
	failures      chan error
	subscriptions int
}

func (d *eventDocker) Events(_ context.Context, opts events.ListOptions) (<-chan events.Message, <-chan error) {
	d.subscriptions++
	return d.messages, d.failures
}

func TestComposeHostEventsTriggerInventory(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		d := &eventDocker{fakeDocker: fixture(), messages: make(chan events.Message), failures: make(chan error)}
		inv := New(d)
		updates := make(chan struct{}, 4)
		go inv.WatchChanges(ctx, updates)
		synctest.Wait()
		emit := func(action string) {
			d.messages <- events.Message{Type: events.ContainerEventType, Action: events.Action(action)}
		}
		emit("exec_start: ls")
		time.Sleep(3 * time.Second)
		if len(updates) != 0 {
			t.Fatal("exec woke resource collection")
		}
		d.containers[0].Labels = map[string]string{"com.docker.compose.project": "site", "com.docker.compose.service": "web"}
		emit("create")
		emit("start")
		time.Sleep(3 * time.Second)
		synctest.Wait()
		if len(updates) != 1 {
			t.Fatalf("up notifications: %d", len(updates))
		}
		<-updates
		if projects := inv.Refresh(ctx).ComposeProjects; len(projects) != 1 || projects[0].Name != "site" {
			t.Fatal(projects)
		}
		d.failures <- io.EOF
		d.containers = nil // docker compose down while the event stream is disconnected
		time.Sleep(11 * time.Second)
		synctest.Wait()
		if d.subscriptions != 2 {
			t.Fatal("did not reconnect")
		}
		if len(updates) != 1 {
			t.Fatal("reconnect did not reconcile changes missed while disconnected")
		}
		<-updates
		if projects := inv.Refresh(ctx).ComposeProjects; len(projects) != 0 {
			t.Fatal(projects)
		}
		emit("die")
		emit("destroy")
		time.Sleep(3 * time.Second)
		synctest.Wait()
		if len(updates) == 0 {
			t.Fatal("down did not request a refresh")
		}
		<-updates
		d.messages <- events.Message{Type: events.ImageEventType, Action: events.ActionCreate}
		time.Sleep(3 * time.Second)
		synctest.Wait()
		if len(updates) != 1 {
			t.Fatal("locally built image did not request a refresh")
		}
		if projects := inv.Refresh(ctx).ComposeProjects; len(projects) != 0 {
			t.Fatal(projects)
		}
	})
}
