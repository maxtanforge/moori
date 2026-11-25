// Package observability stores current host health independently of deployments.
package observability

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"time"

	"github.com/maxtanforge/moori/internal/core/telemetry"
	"github.com/maxtanforge/moori/internal/platform/sqlite"
)

const freshness = 90 * time.Second
const minimumWriteInterval = 10 * time.Second

//go:embed schema.sql
var schema string

//go:embed inventory.sql
var inventorySchema string

type Store struct {
	db         *sql.DB
	latestOnly bool
}

// OpenNodeState stores operational contact and inventory separately from
// optional metric history and alerts.
func OpenNodeState(ctx context.Context, path string) (*Store, error) {
	db, err := sqlite.Open(ctx, path, 0x4d4f4f4e, schema, inventorySchema)
	if err != nil {
		return nil, err
	}
	return &Store{db: db, latestOnly: true}, nil
}

func Open(ctx context.Context, path string) (*Store, error) {
	db, err := sqlite.Open(ctx, path, 0x4d4f4f4d, schema, incidentSchema, inventorySchema, historySchema, alertSchema, alertRecipientSchema, alertEventsIndexSchema)
	if err != nil {
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func validPeer(agent string, generation int64) bool {
	return agent != "" && len(agent) <= 128 && generation > 0
}

func (s *Store) RecordContact(ctx context.Context, agent string, generation int64, at time.Time) error {
	if !validPeer(agent, generation) || at.UnixMilli() <= 0 {
		return errors.New("invalid Agent contact")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO host_contacts VALUES (?,?,?)
 ON CONFLICT(agent) DO UPDATE SET generation=excluded.generation,at_ms=excluded.at_ms
 WHERE excluded.generation>host_contacts.generation OR
 (excluded.generation=host_contacts.generation AND excluded.at_ms-host_contacts.at_ms>=?)`,
		agent, generation, at.UnixMilli(), minimumWriteInterval.Milliseconds())
	return err
}

func (s *Store) RecordSample(ctx context.Context, agent string, generation int64, sample telemetry.Sample, at time.Time) error {
	if !validPeer(agent, generation) || at.UnixMilli() <= 0 || !sample.Valid() {
		return errors.New("invalid Agent resource sample")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO host_samples VALUES (?,?,?,?,?,?,?,?)
 ON CONFLICT(agent) DO UPDATE SET generation=excluded.generation,at_ms=excluded.at_ms,cpu=excluded.cpu,
 memory_bytes=excluded.memory_bytes,memory_available_bytes=excluded.memory_available_bytes,
 disk_bytes=excluded.disk_bytes,disk_available_bytes=excluded.disk_available_bytes
 WHERE excluded.generation>host_samples.generation OR
 (excluded.generation=host_samples.generation AND excluded.at_ms-host_samples.at_ms>=?)`,
		agent, generation, at.UnixMilli(), sample.CPU, sample.MemoryBytes, sample.MemoryAvailableBytes, sample.DiskBytes, sample.DiskAvailableBytes, minimumWriteInterval.Milliseconds())
	return err
}

type Health struct {
	Agent       string            `json:"agent"`
	Generation  int64             `json:"generation"`
	Contact     string            `json:"contact"`
	Metrics     string            `json:"metrics"`
	LastContact *time.Time        `json:"last_contact,omitempty"`
	SampledAt   *time.Time        `json:"sampled_at,omitempty"`
	Sample      *telemetry.Sample `json:"sample,omitempty"`
	Alerts      []string          `json:"alerts"`
	Incidents   []Incident        `json:"incidents"`
}

func (s *Store) Health(ctx context.Context, agent string, generation int64, now time.Time) (Health, error) {
	view, err := readHealth(ctx, s.db, agent, generation, now)
	if err != nil {
		return Health{}, err
	}
	if s.latestOnly {
		return view, nil
	}
	view.Incidents, err = s.incidents(ctx, agent, generation)
	return view, err
}

type healthReader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func readHealth(ctx context.Context, q healthReader, agent string, generation int64, now time.Time) (Health, error) {
	if !validPeer(agent, generation) {
		return Health{}, errors.New("invalid Agent identity")
	}
	h := Health{Agent: agent, Generation: generation, Contact: "never_seen", Metrics: "missing", Alerts: make([]string, 0, 2)}
	var contact, observed, cpu sql.NullInt64
	var sample telemetry.Sample
	err := q.QueryRowContext(ctx, `SELECT c.at_ms,m.at_ms,m.cpu,coalesce(m.memory_bytes,0),
 coalesce(m.memory_available_bytes,0),coalesce(m.disk_bytes,0),coalesce(m.disk_available_bytes,0)
 FROM (SELECT 1) LEFT JOIN host_contacts c ON c.agent=? AND c.generation=?
 LEFT JOIN host_samples m ON m.agent=? AND m.generation=?`, agent, generation, agent, generation).Scan(
		&contact, &observed, &cpu, &sample.MemoryBytes, &sample.MemoryAvailableBytes, &sample.DiskBytes, &sample.DiskAvailableBytes)
	if err != nil {
		return Health{}, err
	}
	if contact.Valid {
		at := time.UnixMilli(contact.Int64).UTC()
		h.LastContact = &at
		h.Contact = "online"
		if now.Sub(at) > freshness {
			h.Contact = "offline"
			h.Alerts = append(h.Alerts, "agent_offline")
		}
	}
	if observed.Valid {
		at := time.UnixMilli(observed.Int64).UTC()
		h.SampledAt = &at
		if cpu.Valid {
			value := uint16(cpu.Int64)
			sample.CPU = &value
		}
		h.Sample = &sample
		h.Metrics = "stale"
		if h.Contact == "online" && now.Sub(at) <= freshness {
			h.Metrics = "current"
			if sample.DiskAvailableBytes <= sample.DiskBytes/10 {
				h.Alerts = append(h.Alerts, "disk_low")
			}
		}
	}
	return h, nil
}
