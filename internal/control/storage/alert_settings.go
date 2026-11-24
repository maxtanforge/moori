package storage

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// maxAlertTargets bounds webhook fan-out per alert delivery.
const maxAlertTargets = 10

// AlertSettings is the console-managed alert policy override. An absent row
// means the engine applies its bootstrap defaults from the environment.
type AlertSettings struct {
	CPUPercent       int       `json:"cpu_percent"`
	MemoryPercent    int       `json:"memory_percent"`
	SustainedSeconds int       `json:"sustained_seconds"`
	UpdatedBy        string    `json:"updated_by,omitempty"`
	UpdatedAt        time.Time `json:"updated_at,omitempty"`
}

// AlertTarget is one webhook destination for alert transition deliveries.
type AlertTarget struct {
	ID        string    `json:"id"`
	URL       string    `json:"url"`
	CreatedBy string    `json:"created_by,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// ValidateAlertTarget enforces the webhook destination rules: an absolute
// HTTP(S) URL without credentials or a fragment, short enough to audit.
func ValidateAlertTarget(rawURL string) error {
	if rawURL == "" || len(rawURL) > 512 {
		return errors.New("must be an HTTP(S) URL of at most 512 characters")
	}
	if strings.ContainsFunc(rawURL, func(r rune) bool { return r <= ' ' || r == 0x7f }) {
		return errors.New("must not contain whitespace or control characters")
	}
	endpoint, err := url.Parse(rawURL)
	if err != nil || (endpoint.Scheme != "http" && endpoint.Scheme != "https") ||
		endpoint.Hostname() == "" || endpoint.User != nil || endpoint.Fragment != "" {
		return errors.New("must be an absolute HTTP(S) URL without credentials or a fragment")
	}
	return nil
}

// AlertSettings returns the persisted policy override; ok is false when the
// engine defaults apply. UpdatedBy resolves the actor's username, empty when
// that account no longer exists.
func (s *Store) AlertSettings(ctx context.Context) (AlertSettings, bool, error) {
	var settings AlertSettings
	var sustainedMs, updatedMs int64
	var updatedBy sql.Null[string]
	err := s.db.QueryRowContext(ctx, `SELECT s.cpu_percent,s.memory_percent,s.sustained_ms,u.username,s.updated_ms
	 FROM alert_settings s LEFT JOIN human_users u ON u.id=s.updated_by WHERE s.id=1`).
		Scan(&settings.CPUPercent, &settings.MemoryPercent, &sustainedMs, &updatedBy, &updatedMs)
	if errors.Is(err, sql.ErrNoRows) {
		return AlertSettings{}, false, nil
	}
	if err != nil {
		return AlertSettings{}, false, err
	}
	settings.UpdatedBy = updatedBy.V
	settings.SustainedSeconds = int(sustainedMs / int64(time.Second/time.Millisecond))
	settings.UpdatedAt = time.UnixMilli(updatedMs).UTC()
	return settings, true, nil
}

// SetAlertSettings upserts the singleton policy override (platform administrators).
func (s *Store) SetAlertSettings(ctx context.Context, session string, settings AlertSettings) error {
	if settings.CPUPercent < 1 || settings.CPUPercent > 100 ||
		settings.MemoryPercent < 1 || settings.MemoryPercent > 100 ||
		settings.SustainedSeconds < 1 || settings.SustainedSeconds > 3600 {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	actor, err := platformActor(ctx, tx, session)
	if err != nil {
		return err
	}
	if err := requireRecoveryComplete(ctx, tx); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO alert_settings(id,cpu_percent,memory_percent,sustained_ms,updated_by,updated_ms)
	 VALUES (1,?,?,?,?,?)
	 ON CONFLICT(id) DO UPDATE SET cpu_percent=excluded.cpu_percent,memory_percent=excluded.memory_percent,
	  sustained_ms=excluded.sustained_ms,updated_by=excluded.updated_by,updated_ms=excluded.updated_ms`,
		settings.CPUPercent, settings.MemoryPercent, settings.SustainedSeconds*int(time.Second/time.Millisecond),
		actor, time.Now().UnixMilli()); err != nil {
		return err
	}
	detail := fmt.Sprintf("cpu=%d%% memory=%d%% sustained=%ds",
		settings.CPUPercent, settings.MemoryPercent, settings.SustainedSeconds)
	if err := addProjectAudit(ctx, tx, "", "alert.settings_updated", actor, "alert-policy", detail); err != nil {
		return err
	}
	return tx.Commit()
}

// ClearAlertSettings removes the persisted override so the defaults apply again.
func (s *Store) ClearAlertSettings(ctx context.Context, session string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	actor, err := platformActor(ctx, tx, session)
	if err != nil {
		return err
	}
	if err := requireRecoveryComplete(ctx, tx); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, "DELETE FROM alert_settings WHERE id=1")
	if err != nil {
		return err
	}
	changed, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		return tx.Commit()
	}
	if err := addProjectAudit(ctx, tx, "", "alert.settings_cleared", actor, "alert-policy", "restored default thresholds"); err != nil {
		return err
	}
	return tx.Commit()
}

// AlertTargets lists configured webhook destinations (platform administrators).
func (s *Store) AlertTargets(ctx context.Context, session string) ([]AlertTarget, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := platformActor(ctx, tx, session); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT t.id,t.url,CASE WHEN t.created_by='environment' THEN 'environment' ELSE u.username END,t.created_ms
	 FROM alert_targets t LEFT JOIN human_users u ON u.id=t.created_by ORDER BY t.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	targets := make([]AlertTarget, 0, maxAlertTargets)
	for rows.Next() {
		var target AlertTarget
		var createdMs int64
		var createdBy sql.Null[string]
		if err := rows.Scan(&target.ID, &target.URL, &createdBy, &createdMs); err != nil {
			return nil, err
		}
		target.CreatedBy = createdBy.V
		target.CreatedAt = time.UnixMilli(createdMs).UTC()
		targets = append(targets, target)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return targets, tx.Commit()
}

// CreateAlertTarget stores another webhook destination for alert deliveries.
// Each destination retries independently with the original event ID.
func (s *Store) CreateAlertTarget(ctx context.Context, session, rawURL string) (AlertTarget, error) {
	rawURL = strings.TrimSpace(rawURL)
	if err := ValidateAlertTarget(rawURL); err != nil {
		return AlertTarget{}, fmt.Errorf("%w: webhook URL %v", ErrInvalid, err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AlertTarget{}, err
	}
	defer tx.Rollback()
	actor, err := platformActor(ctx, tx, session)
	if err != nil {
		return AlertTarget{}, err
	}
	if err := requireRecoveryComplete(ctx, tx); err != nil {
		return AlertTarget{}, err
	}
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM alert_targets").Scan(&count); err != nil {
		return AlertTarget{}, err
	}
	if count >= maxAlertTargets {
		return AlertTarget{}, fmt.Errorf("%w: at most %d notification targets are kept", ErrConflict, maxAlertTargets)
	}
	// created_by stores the actor ID for referential integrity; responses
	// resolve it to the username for display.
	var createdBy string
	if err := tx.QueryRowContext(ctx, "SELECT username FROM human_users WHERE id=?", actor).Scan(&createdBy); err != nil {
		return AlertTarget{}, err
	}
	target := AlertTarget{ID: "alert-" + rand.Text(), URL: rawURL, CreatedBy: createdBy, CreatedAt: time.Now().UTC()}
	if _, err := tx.ExecContext(ctx, `INSERT INTO alert_targets(id,url,created_by,created_ms) VALUES (?,?,?,?)`,
		target.ID, target.URL, actor, target.CreatedAt.UnixMilli()); err != nil {
		return AlertTarget{}, err
	}
	if err := addProjectAudit(ctx, tx, "", "alert.target_created", actor, target.ID, auditTargetDetail(rawURL)); err != nil {
		return AlertTarget{}, err
	}
	return target, tx.Commit()
}

// DeleteAlertTarget removes a webhook destination; deliveries to the remaining
// destinations continue unchanged.
func (s *Store) DeleteAlertTarget(ctx context.Context, session, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	actor, err := platformActor(ctx, tx, session)
	if err != nil {
		return err
	}
	if err := requireRecoveryComplete(ctx, tx); err != nil {
		return err
	}
	var rawURL string
	if err := tx.QueryRowContext(ctx, "SELECT url FROM alert_targets WHERE id=?", id).Scan(&rawURL); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, "DELETE FROM alert_targets WHERE id=?", id)
	if err := expectOne(res, err, sql.ErrNoRows); err != nil {
		return err
	}
	if err := addProjectAudit(ctx, tx, "", "alert.target_deleted", actor, id, auditTargetDetail(rawURL)); err != nil {
		return err
	}
	return tx.Commit()
}

// AlertNotificationTargets returns the destinations for the delivery loop.
func (s *Store) AlertNotificationTargets(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT url FROM alert_targets ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	targets := make([]string, 0, maxAlertTargets)
	for rows.Next() {
		var rawURL string
		if err := rows.Scan(&rawURL); err != nil {
			return nil, err
		}
		targets = append(targets, rawURL)
	}
	return targets, rows.Err()
}

// EnsureAlertTarget imports the environment webhook once, when no console
// target exists yet, so upgrades keep delivering without a restart. It reports
// whether an import happened; an empty URL is a no-op.
func (s *Store) EnsureAlertTarget(ctx context.Context, rawURL string) (bool, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return false, nil
	}
	if err := ValidateAlertTarget(rawURL); err != nil {
		return false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM alert_targets").Scan(&count); err != nil {
		return false, err
	}
	if count > 0 {
		return false, tx.Commit()
	}
	id := "alert-" + rand.Text()
	if _, err := tx.ExecContext(ctx, `INSERT INTO alert_targets(id,url,created_by,created_ms) VALUES (?,?,?,?)`,
		id, rawURL, "environment", time.Now().UnixMilli()); err != nil {
		return false, err
	}
	detail := "imported from MOORI_ALERT_WEBHOOK " + auditTargetDetail(rawURL)
	if err := addProjectAudit(ctx, tx, "", "alert.target_created", "system", id, detail); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// auditTargetDetail keeps token-bearing paths and queries out of the audit log:
// only the origin is recorded there, the full URL lives in alert_targets.
func auditTargetDetail(rawURL string) string {
	endpoint, err := url.Parse(rawURL)
	if err != nil {
		return "(unparsed destination)"
	}
	detail := endpoint.Scheme + "://" + endpoint.Host
	if endpoint.Path != "" || endpoint.RawQuery != "" {
		detail += "/…"
	}
	return detail
}
