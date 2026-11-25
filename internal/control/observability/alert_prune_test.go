package observability

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/maxtanforge/moori/internal/platform/sqlite"
)

func TestPruneAlertEventsKeepsPendingDeliveries(t *testing.T) {
	ctx := t.Context()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "metrics.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now()
	day := 24 * time.Hour
	for _, e := range []struct {
		id    int64
		gen   int64
		code  string
		state string
		age   time.Duration
	}{
		{1, 1, "disk_low", "fired", 31 * day},
		{2, 1, "cpu_high", "fired", 31 * day},
		{3, 1, "memory_high", "recovered", 2 * day},
		{4, 2, "disk_low", "fired", 31 * day},
	} {
		if _, err := store.db.ExecContext(ctx, `INSERT INTO alert_events(id,agent,generation,code,state,at_ms)
		 VALUES(?,'node',?,?,?,?)`, e.id, e.gen, e.code, e.state, now.Add(-e.age).UnixMilli()); err != nil {
			t.Fatal(err)
		}
	}
	// A pending delivery keeps its event alive past the retention window; the
	// other stale events (including one from a superseded generation) age out.
	if _, err := store.db.ExecContext(ctx, `INSERT INTO alert_deliveries(event_id,attempts,available_ms,expires_ms)
	 VALUES(2,1,?,?)`, now.UnixMilli(), now.Add(time.Hour).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if err := store.PruneAlertEvents(ctx, now); err != nil {
		t.Fatal(err)
	}
	assertEventIDs(t, store, []int64{2, 3})

	// Once the delivery reaches a terminal state the event ages out normally.
	if _, err := store.db.ExecContext(ctx, "UPDATE alert_deliveries SET expires_ms=? WHERE event_id=2", now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimAlertDelivery(ctx, now.Add(time.Minute))
	if err != nil || claimed != nil {
		t.Fatalf("expired lease was claimed: %+v, %v", claimed, err)
	}
	if err := store.PruneAlertEvents(ctx, now); err != nil {
		t.Fatal(err)
	}
	assertEventIDs(t, store, []int64{3})
}

func assertEventIDs(t *testing.T, store *Store, want []int64) {
	t.Helper()
	rows, err := store.db.QueryContext(t.Context(), "SELECT id FROM alert_events ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		got = append(got, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("surviving events %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("surviving events %v, want %v", got, want)
		}
	}
}

// The suppress and age indexes must reach databases created before the
// migration, not only fresh installs.
func TestAlertEventsIndexMigration(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "metrics.db")
	old, err := sqlite.Open(ctx, path, 0x4d4f4f4d, schema, incidentSchema, inventorySchema, historySchema, alertSchema, alertRecipientSchema)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.ExecContext(ctx, `INSERT INTO alert_events(id,agent,generation,code,state,at_ms)
	 VALUES(1,'node',1,'cpu_high','fired',123)`); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var count int
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema
	 WHERE type='index' AND name IN ('alert_events_suppress','alert_events_at')`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("expected both alert event indexes after migration, found %d", count)
	}
	events, err := store.AlertHistory(ctx, "node", 1, 10)
	if err != nil || len(events) != 1 || events[0].Code != "cpu_high" {
		t.Fatalf("existing events after migration: %+v, %v", events, err)
	}
}
