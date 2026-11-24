package observability

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/maxtanforge/moori/internal/platform/sqlite"
)

func TestAlertRecipientMigrationKeepsPendingEvent(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "metrics.db")
	old, err := sqlite.Open(ctx, path, 0x4d4f4f4d, schema, incidentSchema, inventorySchema, historySchema, alertSchema)
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	now := time.Now()
	if _, err := old.ExecContext(ctx, `INSERT INTO alert_events(id,agent,generation,code,state,at_ms) VALUES(1,'node',1,'disk_low','fired',?)`, now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if _, err := old.ExecContext(ctx, `INSERT INTO alert_deliveries(event_id,attempts,available_ms,expires_ms) VALUES(1,1,?,?)`, now.UnixMilli(), now.Add(time.Hour).UnixMilli()); err != nil {
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
	if err := store.ExpandAlertRecipients(ctx, []string{"first", "second"}, now); err != nil {
		t.Fatal(err)
	}
	for _, recipient := range []string{"first", "second"} {
		delivery, err := store.ClaimAlertRecipient(ctx, now)
		if err != nil || delivery == nil || delivery.ID != 1 || delivery.Recipient != recipient || delivery.Attempt != 2 {
			t.Fatalf("delivery after migration: %+v, %v", delivery, err)
		}
	}
}
