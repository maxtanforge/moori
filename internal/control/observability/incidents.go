package observability

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"time"
)

//go:embed incidents.sql
var incidentSchema string

type Incident struct {
	Code     string    `json:"code"`
	OpenedAt time.Time `json:"opened_at"`
}

func (s *Store) Evaluate(ctx context.Context, agent string, generation int64, now time.Time) error {
	return s.EvaluateWithPolicy(ctx, agent, generation, now, DefaultPolicy())
}

// EvaluateWithPolicy advances incidents using fresh evidence for the supplied Agent generation.
func (s *Store) EvaluateWithPolicy(ctx context.Context, agent string, generation int64, now time.Time, policy Policy) error {
	if !validPeer(agent, generation) || now.UnixMilli() <= 0 {
		return errors.New("invalid incident evaluation")
	}
	if !policy.valid() {
		return errors.New("invalid alert policy")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	evaluator := evaluation{tx: tx, agent: agent, generation: generation, now: now, policy: policy}
	current, err := evaluator.currentGeneration(ctx)
	if err != nil || !current {
		return err
	}
	status, err := readHealth(ctx, tx, agent, generation, now)
	if err != nil {
		return err
	}
	if status.LastContact != nil && !status.LastContact.After(now) {
		if err := evaluator.setIncident(ctx, "agent_offline", status.Contact == "offline"); err != nil {
			return err
		}
	}
	fresh := status.Metrics == "current" && !status.LastContact.After(now) &&
		!status.SampledAt.After(now) && status.Sample.Valid()
	if !fresh {
		if err := evaluator.clearWindow(ctx, "cpu_high"); err != nil {
			return err
		}
		if err := evaluator.clearWindow(ctx, "memory_high"); err != nil {
			return err
		}
		return tx.Commit()
	}
	sample := status.Sample
	switch {
	case sample.DiskAvailableBytes <= percentOf(sample.DiskBytes, 10, false):
		err = evaluator.setIncident(ctx, "disk_low", true)
	case sample.DiskAvailableBytes >= percentOf(sample.DiskBytes, 15, true):
		err = evaluator.setIncident(ctx, "disk_low", false)
	}
	if err != nil {
		return err
	}
	switch {
	case sample.CPU == nil:
		err = evaluator.clearWindow(ctx, "cpu_high")
	case int(*sample.CPU) >= policy.CPUPercent*100:
		err = evaluator.evaluateWindow(ctx, "cpu_high", policy.CPUPercent, *status.SampledAt)
	default:
		err = evaluator.belowThreshold(ctx, "cpu_high", belowRecovery(*sample.CPU, policy.CPUPercent))
	}
	if err != nil {
		return err
	}
	used := sample.MemoryBytes - sample.MemoryAvailableBytes
	if used >= percentOf(sample.MemoryBytes, policy.MemoryPercent, true) {
		err = evaluator.evaluateWindow(ctx, "memory_high", policy.MemoryPercent, *status.SampledAt)
	} else {
		recovery := max(0, policy.MemoryPercent-alertRecoveryMargin)
		err = evaluator.belowThreshold(ctx, "memory_high", used <= percentOf(sample.MemoryBytes, recovery, false))
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

type evaluation struct {
	tx         *sql.Tx
	agent      string
	generation int64
	now        time.Time
	policy     Policy
}

func (e evaluation) currentGeneration(ctx context.Context) (bool, error) {
	var evaluated, contacted, sampled int64
	err := e.tx.QueryRowContext(ctx, `SELECT
	 coalesce((SELECT generation FROM alert_generations WHERE agent=?),0),
	 coalesce((SELECT generation FROM host_contacts WHERE agent=?),0),
	 coalesce((SELECT generation FROM host_samples WHERE agent=?),0)`, e.agent, e.agent, e.agent).
		Scan(&evaluated, &contacted, &sampled)
	if err != nil || e.generation < max(evaluated, contacted, sampled) {
		return false, err
	}
	if evaluated == e.generation {
		return true, nil
	}
	if _, err := e.tx.ExecContext(ctx, `INSERT INTO alert_generations VALUES (?,?)
	 ON CONFLICT(agent) DO UPDATE SET generation=excluded.generation`, e.agent, e.generation); err != nil {
		return false, err
	}
	// A newer enrollment invalidates old evidence, without implying recovery.
	if _, err := e.tx.ExecContext(ctx, "DELETE FROM active_incidents WHERE agent=? AND generation<?", e.agent, e.generation); err != nil {
		return false, err
	}
	if _, err := e.tx.ExecContext(ctx, "DELETE FROM alert_breach_windows WHERE agent=? AND generation<?", e.agent, e.generation); err != nil {
		return false, err
	}
	_, err = e.tx.ExecContext(ctx, `DELETE FROM alert_deliveries WHERE event_id IN (
	 SELECT id FROM alert_events WHERE agent=? AND generation<?)`, e.agent, e.generation)
	return err == nil, err
}

func (e evaluation) setIncident(ctx context.Context, code string, active bool) error {
	var result sql.Result
	var err error
	state := "recovered"
	if active {
		state = "fired"
		result, err = e.tx.ExecContext(ctx, `INSERT INTO active_incidents(agent,generation,code,opened_ms) VALUES (?,?,?,?)
		 ON CONFLICT(agent,code) DO NOTHING`, e.agent, e.generation, code, e.now.UnixMilli())
	} else {
		result, err = e.tx.ExecContext(ctx, "DELETE FROM active_incidents WHERE agent=? AND generation=? AND code=?",
			e.agent, e.generation, code)
	}
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed == 0 {
		return err
	}
	return recordAlertEvent(ctx, e.tx, e.agent, e.generation, code, state, e.now, e.policy.Notify)
}

func (e evaluation) clearWindow(ctx context.Context, code string) error {
	_, err := e.tx.ExecContext(ctx, "DELETE FROM alert_breach_windows WHERE agent=? AND generation=? AND code=?",
		e.agent, e.generation, code)
	return err
}

func (e evaluation) belowThreshold(ctx context.Context, code string, recovered bool) error {
	if err := e.clearWindow(ctx, code); err != nil || !recovered {
		return err
	}
	return e.setIncident(ctx, code, false)
}

// A window starts when a current sample breaches a threshold. Subsequent accepted
// samples extend it in the database trigger, including samples between evaluations.
func (e evaluation) evaluateWindow(ctx context.Context, code string, percent int, observed time.Time) error {
	at := observed.UnixMilli()
	var first int64
	err := e.tx.QueryRowContext(ctx, `SELECT started_ms FROM alert_breach_windows
	 WHERE agent=? AND generation=? AND code=? AND percent=? AND last_ms=?`,
		e.agent, e.generation, code, percent, at).Scan(&first)
	if errors.Is(err, sql.ErrNoRows) {
		_, err = e.tx.ExecContext(ctx, `INSERT INTO alert_breach_windows(agent,generation,code,percent,started_ms,last_ms,gap_ms)
		 SELECT ?,?,?,?,?,?,? WHERE NOT EXISTS (SELECT 1 FROM active_incidents WHERE agent=? AND code=?)
		 ON CONFLICT(agent,code) DO UPDATE SET generation=excluded.generation, percent=excluded.percent,
		 started_ms=excluded.started_ms,last_ms=excluded.last_ms,gap_ms=excluded.gap_ms`,
			e.agent, e.generation, code, percent, at, at, freshness.Milliseconds(), e.agent, code)
		return err
	}
	if err != nil || at-first < e.policy.SustainedFor.Milliseconds() {
		return err
	}
	if err := e.setIncident(ctx, code, true); err != nil {
		return err
	}
	return e.clearWindow(ctx, code)
}

func (s *Store) incidents(ctx context.Context, agent string, generation int64) ([]Incident, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT code,opened_ms FROM active_incidents
 WHERE agent=? AND generation=? ORDER BY code`, agent, generation)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]Incident, 0, 4)
	for rows.Next() {
		var i Incident
		var opened int64
		if err := rows.Scan(&i.Code, &opened); err != nil {
			return nil, err
		}
		i.OpenedAt = time.UnixMilli(opened).UTC()
		result = append(result, i)
	}
	return result, rows.Err()
}
