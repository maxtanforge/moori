// Package alertnotify delivers durable alert transitions to the administrator's
// webhook destinations.
package alertnotify

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/maxtanforge/moori/internal/control/observability"
)

type Notifier struct {
	targets func(context.Context) ([]string, error)
	client  http.Client
	// NodeActive is checked immediately before sending a leased event.
	// An inactive identity's alert stays in history without being sent.
	NodeActive func(context.Context, string, int64) (bool, error)
}

// New accepts a provider of administrator-configured HTTP(S) destinations, so
// console changes apply without a restart.
func New(targets func(context.Context) ([]string, error)) *Notifier {
	return &Notifier{
		targets: targets,
		client: http.Client{
			Timeout: 5 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

// Run delivers pending alerts until ctx is canceled. Once the queue drains it
// idles on a read-only wait query instead of claiming on a fixed tick, so an
// idle deployment no longer takes a write lock every few seconds. Fresh alerts
// still surface promptly: evaluation inserts at a 15 second cadence, which the
// idle floor matches.
func (n *Notifier) Run(ctx context.Context, store *observability.Store) {
	const idleFloor = 15 * time.Second
	for ctx.Err() == nil {
		worked := false
		for range 16 {
			next, err := n.deliverNext(ctx, store, time.Now())
			if err != nil && ctx.Err() == nil {
				slog.Warn("could not update pending alert notification", "err", err)
			}
			if err != nil || !next {
				break
			}
			worked = true
		}
		if worked {
			continue
		}
		delay := idleFloor
		if wait, ok, err := store.NextAlertDeliveryWait(ctx, time.Now()); err != nil {
			if ctx.Err() == nil {
				slog.Warn("could not check pending alert notifications", "err", err)
			}
			delay = 5 * time.Second
		} else if ok {
			delay = min(max(wait, time.Second), time.Minute)
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (n *Notifier) deliverNext(ctx context.Context, store *observability.Store, now time.Time) (bool, error) {
	started := time.Now()
	targets, err := n.targets(ctx)
	if err != nil {
		return false, err
	}
	urls := make(map[string]string, len(targets))
	recipients := make([]string, 0, len(targets))
	for _, target := range targets {
		hash := sha256.Sum256([]byte(target))
		id := hex.EncodeToString(hash[:])
		urls[id] = target
		recipients = append(recipients, id)
	}
	if err := store.ExpandAlertRecipients(ctx, recipients, now); err != nil {
		return false, err
	}
	delivery, err := store.ClaimAlertRecipient(ctx, now)
	if err != nil || delivery == nil {
		return false, err
	}
	if n.NodeActive != nil {
		active, err := n.NodeActive(ctx, delivery.Agent, delivery.Generation)
		if err != nil {
			return true, store.FinishAlertDelivery(ctx, *delivery, now, false)
		}
		if !active {
			return true, store.FinishAlertDelivery(ctx, *delivery, now, true)
		}
	}
	target := urls[delivery.Recipient]
	body, encodeErr := json.Marshal(delivery)
	delivered := target == "" || encodeErr == nil && n.post(ctx, target, body, delivery.ID)
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	if !delivered {
		slog.Warn("alert webhook delivery failed", "event_id", delivery.ID, "attempt", delivery.Attempt)
	}
	return true, store.FinishAlertDelivery(ctx, *delivery, now.Add(time.Since(started)), delivered)
}

func (n *Notifier) post(ctx context.Context, target string, body []byte, eventID int64) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", strconv.FormatInt(eventID, 10))
	// Keep retries in the durable queue, including requests on reused connections.
	req.GetBody = nil
	response, err := n.client.Do(req)
	if err != nil {
		return false
	}
	response.Body.Close()
	return response.StatusCode >= 200 && response.StatusCode < 300
}
