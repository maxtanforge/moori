package alertnotify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/maxtanforge/moori/internal/control/observability"
	"github.com/maxtanforge/moori/internal/core/telemetry"
)

func staticTargets(urls ...string) func(context.Context) ([]string, error) {
	return func(context.Context) ([]string, error) { return urls, nil }
}

func TestDeliveryRetriesAcrossRestartWithSameEventID(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	path := filepath.Join(t.TempDir(), "metrics.db")
	store := openStore(t, ctx, path)
	policy := observability.DefaultPolicy()
	policy.Notify = true
	fireDiskAlert(t, ctx, store, now, policy)
	fireDiskAlert(t, ctx, store, now, policy)
	var mu sync.Mutex
	var received []string
	attempt := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var event struct {
			observability.AlertEvent
			Agent      string `json:"agent"`
			Generation int64  `json:"generation"`
		}
		if err := json.NewDecoder(req.Body).Decode(&event); err != nil ||
			req.Method != http.MethodPost || event.State != "fired" || event.Code != "disk_low" ||
			event.ID <= 0 || !event.At.Equal(now) || event.Agent != "agent" || event.Generation != 1 {
			t.Errorf("event = %+v, err = %v", event, err)
		}
		mu.Lock()
		defer mu.Unlock()
		received = append(received, req.Header.Get("Idempotency-Key"))
		if len(received) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer attempt.Close()
	notifier := New(staticTargets(attempt.URL))
	if _, err := notifier.deliverNext(ctx, store, now); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = openStore(t, ctx, path)
	notifier = New(staticTargets(attempt.URL))
	for _, offset := range []time.Duration{time.Second, time.Minute, time.Hour} {
		if _, err := notifier.deliverNext(ctx, store, now.Add(offset)); err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(received) != 2 || received[0] == "" || received[0] != received[1] {
		t.Fatalf("idempotency keys = %v", received)
	}
}

func TestDeliveryRetriesOnlyFailingTargetWithoutBlockingRecovery(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	store := openStore(t, ctx, filepath.Join(t.TempDir(), "metrics.db"))
	policy := observability.DefaultPolicy()
	policy.Notify = true
	fireDiskAlert(t, ctx, store, now, policy)
	var mu sync.Mutex
	counts := map[string]int{}
	keys := map[string][]string{}
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		counts["failing"]++
		keys["failing"] = append(keys["failing"], req.Header.Get("Idempotency-Key"))
		mu.Unlock()
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer failing.Close()
	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		counts["healthy"]++
		keys["healthy"] = append(keys["healthy"], req.Header.Get("Idempotency-Key"))
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer healthy.Close()
	notifier := New(staticTargets(failing.URL, healthy.URL))
	for range 2 {
		if proceeded, err := notifier.deliverNext(ctx, store, now); err != nil || !proceeded {
			t.Fatal(proceeded, err)
		}
	}
	recovered := now.Add(15 * time.Second)
	sample := telemetry.Sample{MemoryBytes: 100, MemoryAvailableBytes: 50, DiskBytes: 100, DiskAvailableBytes: 50}
	if err := store.RecordSample(ctx, "agent", 1, sample, recovered); err != nil {
		t.Fatal(err)
	}
	if err := store.EvaluateWithPolicy(ctx, "agent", 1, recovered, policy); err != nil {
		t.Fatal(err)
	}
	if proceeded, err := notifier.deliverNext(ctx, store, recovered); err != nil || !proceeded {
		t.Fatal(proceeded, err)
	}
	if proceeded, err := notifier.deliverNext(ctx, store, now.Add(31*time.Second)); err != nil || !proceeded {
		t.Fatal(proceeded, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if counts["healthy"] != 2 || counts["failing"] != 2 {
		t.Fatalf("attempts = %v", counts)
	}
	if keys["healthy"][0] == keys["healthy"][1] || keys["healthy"][0] != keys["failing"][0] ||
		keys["failing"][0] != keys["failing"][1] {
		t.Fatalf("idempotency keys = %v", keys)
	}
	// Attempt two backs off another minute, so just before that the event is
	// still waiting for its next round instead of being acknowledged.
	if delivery, err := store.ClaimAlertRecipient(ctx, now.Add(80*time.Second)); err != nil || delivery != nil {
		t.Fatalf("delivery acknowledged despite failing destination: %+v, err = %v", delivery, err)
	}
}

// With no configured destination a queued event is dropped instead of
// retrying until exhaustion, matching the notify gate at enqueue time.
func TestDeliveryWithoutTargetsIsDropped(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	store := openStore(t, ctx, filepath.Join(t.TempDir(), "metrics.db"))
	policy := observability.DefaultPolicy()
	policy.Notify = true
	fireDiskAlert(t, ctx, store, now, policy)
	notifier := New(staticTargets())
	if _, err := notifier.deliverNext(ctx, store, now); err != nil {
		t.Fatal(err)
	}
	if delivery, err := store.ClaimAlertDelivery(ctx, now.Add(time.Minute)); err != nil || delivery != nil {
		t.Fatalf("delivery without targets kept = %+v, err = %v", delivery, err)
	}
	if history, err := store.AlertHistory(ctx, "agent", 1, 100); err != nil || len(history) != 1 {
		t.Fatalf("history = %+v, err = %v", history, err)
	}
}

func TestWebhookDoesNotFollowRedirects(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	store := openStore(t, ctx, filepath.Join(t.TempDir(), "metrics.db"))
	policy := observability.DefaultPolicy()
	policy.Notify = true
	fireDiskAlert(t, ctx, store, now, policy)
	dest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("webhook followed redirect")
	}))
	defer dest.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, dest.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	notifier := New(staticTargets(redirect.URL))
	if _, err := notifier.deliverNext(ctx, store, now); err != nil {
		t.Fatal(err)
	}
}

func TestWebhookDiscardsPendingDeliveryForInactiveNode(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	store := openStore(t, ctx, filepath.Join(t.TempDir(), "metrics.db"))
	policy := observability.DefaultPolicy()
	policy.Notify = true
	fireDiskAlert(t, ctx, store, now, policy)
	dest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("inactive node unexpectedly delivered a webhook")
	}))
	defer dest.Close()
	notifier := New(staticTargets(dest.URL))
	notifier.NodeActive = func(_ context.Context, agent string, generation int64) (bool, error) {
		if agent != "agent" || generation != 1 {
			t.Fatal(agent, generation)
		}
		return false, nil
	}
	if proceeded, err := notifier.deliverNext(ctx, store, now); err != nil || !proceeded {
		t.Fatal(proceeded, err)
	}
	if delivery, err := store.ClaimAlertDelivery(ctx, now.Add(time.Minute)); err != nil || delivery != nil {
		t.Fatal(delivery, err)
	}
	if history, err := store.AlertHistory(ctx, "agent", 1, 100); err != nil || len(history) != 1 {
		t.Fatal(history, err)
	}
}

func openStore(t *testing.T, ctx context.Context, path string) *observability.Store {
	t.Helper()
	store, err := observability.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func fireDiskAlert(t *testing.T, ctx context.Context, store *observability.Store, now time.Time, policy observability.Policy) {
	t.Helper()
	sample := telemetry.Sample{MemoryBytes: 100, MemoryAvailableBytes: 50, DiskBytes: 100, DiskAvailableBytes: 5}
	if err := store.RecordContact(ctx, "agent", 1, now); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordSample(ctx, "agent", 1, sample, now); err != nil {
		t.Fatal(err)
	}
	if err := store.EvaluateWithPolicy(ctx, "agent", 1, now, policy); err != nil {
		t.Fatal(err)
	}
}

func TestWebhookDeliveryExpiresWithoutRemovingHistory(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	store := openStore(t, ctx, filepath.Join(t.TempDir(), "metrics.db"))
	policy := observability.DefaultPolicy()
	policy.Notify = true
	fireDiskAlert(t, ctx, store, now, policy)
	delivery, err := store.ClaimAlertDelivery(ctx, now.Add(24*time.Hour))
	if err != nil || delivery != nil {
		t.Fatalf("expired delivery = %+v, err = %v", delivery, err)
	}
	history, err := store.AlertHistory(ctx, "agent", 1, 100)
	if err != nil || len(history) != 1 {
		t.Fatalf("history = %+v, err = %v", history, err)
	}
}
