package logstream

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var containerID = strings.Repeat("a", 64)

func waitCondition(t *testing.T, ctx context.Context, ready func() bool) {
	t.Helper()
	for !ready() {
		select {
		case <-ctx.Done():
			t.Fatal("broker transition did not occur")
		case <-time.After(time.Millisecond):
		}
	}
}

func startPoll(t *testing.T, ctx context.Context, b *Broker, agent string, generation int64) <-chan Ticket {
	t.Helper()
	out := make(chan Ticket, 1)
	go func() {
		ticket, err := b.Next(ctx, agent, generation)
		if err == nil {
			out <- ticket
		}
		close(out)
	}()
	waitCondition(t, ctx, func() bool {
		b.mu.Lock()
		defer b.mu.Unlock()
		state := b.polls[peer{agent: agent, generation: generation}]
		return state != nil && state.active > 0
	})
	return out
}

func TestLiveLogTicketIsBoundAndClaimedOnce(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	b := new(Broker)
	tickets := startPoll(t, ctx, b, "node", 7)
	consumer := make(chan error, 1)
	received := make(chan string, 1)
	go func() {
		consumer <- b.Receive(ctx, "node", 7, containerID, 200, true, true, func(r io.Reader) error {
			data, err := io.ReadAll(r)
			received <- string(data)
			return err
		})
	}()
	ticket := <-tickets
	if ticket.ID == "" || ticket.Container != containerID || ticket.Tail != 200 || !ticket.Timestamps || !ticket.AllLogRuns {
		t.Fatalf("unexpected log ticket: %+v", ticket)
	}
	junk := &trackedBody{Reader: strings.NewReader("wrong node bytes")}
	if err := b.Send(ctx, "another-node", 7, ticket.ID, junk); !errors.Is(err, ErrUnavailable) || !junk.closed.Load() {
		t.Fatal("different agent was not rejected")
	}
	if err := b.Send(ctx, "node", 8, ticket.ID, io.NopCloser(strings.NewReader("wrong generation bytes"))); !errors.Is(err, ErrUnavailable) {
		t.Fatal("different agent generation was not rejected")
	}
	if err := b.Send(ctx, "node", 7, ticket.ID, io.NopCloser(strings.NewReader("stdout\nstderr\n"))); err != nil {
		t.Fatal(err)
	}
	if err := <-consumer; err != nil || <-received != "stdout\nstderr\n" {
		t.Fatalf("consumer error: %v", err)
	}
	if err := b.Send(ctx, "node", 7, ticket.ID, io.NopCloser(strings.NewReader("late publisher"))); !errors.Is(err, ErrUnavailable) {
		t.Fatal("completed log ticket was reusable")
	}
}

func TestCancellingBrowserClosesNodeReadAndFreesTicket(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	browser, closeBrowser := context.WithCancel(ctx)
	defer closeBrowser()
	b := new(Broker)
	tickets := startPoll(t, ctx, b, "node", 1)
	consumer := make(chan error, 1)
	started := make(chan struct{})
	go func() {
		consumer <- b.Receive(browser, "node", 1, containerID, 0, false, false, func(reader io.Reader) error {
			close(started)
			_, err := io.Copy(io.Discard, reader)
			return err
		})
	}()
	ticket := <-tickets
	reader, writer := io.Pipe()
	defer writer.Close()
	publisher := make(chan error, 1)
	go func() { publisher <- b.Send(ctx, "node", 1, ticket.ID, reader) }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("publisher did not attach")
	}
	closeBrowser()
	if err := <-consumer; !errors.Is(err, context.Canceled) {
		t.Fatalf("browser cancellation = %v", err)
	}
	if err := <-publisher; !errors.Is(err, context.Canceled) {
		t.Fatalf("publisher cancellation = %v", err)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.streams) != 0 {
		t.Fatal("cancelled stream remained registered")
	}
}

func TestUnavailableAndBoundedLiveLogTickets(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	b := new(Broker)
	ignore := func(io.Reader) error { return nil }
	if err := b.Receive(ctx, "node", 1, containerID, 100, false, false, ignore); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unannounced node = %v", err)
	}
	if err := b.Receive(ctx, "node", 1, containerID[:10], 100, false, false, ignore); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid container = %v", err)
	}
	tickets := startPoll(t, ctx, b, "node", 1)
	receivers := make(chan error, maxTickets)
	for range maxTickets {
		go func() { receivers <- b.Receive(ctx, "node", 1, containerID, 100, false, false, ignore) }()
	}
	if ticket := <-tickets; ticket.ID == "" {
		t.Fatal("no operation matched")
	}
	waitCondition(t, ctx, func() bool {
		b.mu.Lock()
		defer b.mu.Unlock()
		return len(b.streams) == maxTickets
	})
	if err := b.Receive(ctx, "node", 1, containerID, 100, false, false, ignore); !errors.Is(err, ErrBusy) {
		t.Fatalf("over-capacity admission = %v", err)
	}
	cancel()
	for range maxTickets {
		if err := <-receivers; !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled pending stream = %v", err)
		}
	}
}

type trackedBody struct {
	io.Reader
	closed atomic.Bool
}

func (r *trackedBody) Close() error { r.closed.Store(true); return nil }

func TestSourceFailureAndOldTicketCannotAffectNewStream(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	b := new(Broker)
	open := func() (Ticket, <-chan error) {
		t.Helper()
		tickets := startPoll(t, ctx, b, "node", 4)
		finished := make(chan error, 1)
		go func() {
			finished <- b.Receive(ctx, "node", 4, containerID, 100, false, false, func(reader io.Reader) error {
				_, err := io.Copy(io.Discard, reader)
				return err
			})
		}()
		return <-tickets, finished
	}
	first, finished := open()
	reader, writer := io.Pipe()
	_ = writer.CloseWithError(io.ErrUnexpectedEOF)
	if err := b.Send(ctx, "node", 4, first.ID, reader); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("publisher error = %v", err)
	}
	if err := <-finished; !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("consumer error = %v", err)
	}

	next, finished := open()
	if first.ID == "" || next.ID == "" || next.ID == first.ID {
		t.Fatal("live stream reused a previous ticket")
	}
	if err := b.Send(ctx, "node", 4, first.ID, io.NopCloser(strings.NewReader("old publisher"))); !errors.Is(err, ErrUnavailable) {
		t.Fatal("a closed ticket attached to a newer stream")
	}
	if err := b.Send(ctx, "node", 4, next.ID, io.NopCloser(strings.NewReader("current publisher"))); err != nil {
		t.Fatal(err)
	}
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
}

func TestStaleAgentPollDoesNotAuthorizeNewLogStream(t *testing.T) {
	b := &Broker{polls: map[peer]*pollState{
		{agent: "node", generation: 2}: {last: time.Now().Add(-pollRecent), wakeup: make(chan struct{})},
	}}
	err := b.Receive(t.Context(), "node", 2, containerID, 100, false, false, func(io.Reader) error { return nil })
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("stale agent polling admitted a stream: %v", err)
	}
}

// A publisher whose receiver has already left its open window must not hold
// the broker mutex while waiting on the undeliverable body: that finish also
// needs the mutex, and with it blocked, every log stream in the process
// freezes. The wait below fails if Send still owns the lock once it has
// claimed the stream.
func TestSendDoesNotHoldBrokerLockWhileUndeliverable(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	b := &Broker{}
	s := &stream{
		Ticket: Ticket{ID: "ticket", Container: containerID, Tail: 100},
		peer:   peer{agent: "node", generation: 1}, claimed: true,
		source: make(chan io.ReadCloser, 1), done: make(chan struct{}),
	}
	b.mu.Lock()
	b.streams = map[string]*stream{s.ID: s}
	b.mu.Unlock()

	sent := make(chan error, 1)
	go func() {
		sent <- b.Send(ctx, "node", 1, s.ID, io.NopCloser(strings.NewReader("body")))
	}()
	waitCondition(t, ctx, func() bool {
		if !b.mu.TryLock() {
			return false
		}
		defer b.mu.Unlock()
		return s.publisher
	})
	b.finish(s, ErrUnavailable)
	if err := <-sent; !errors.Is(err, ErrUnavailable) {
		t.Fatalf("blocked publisher error = %v", err)
	}
}
