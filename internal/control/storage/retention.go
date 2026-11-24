package storage

import (
	"context"
	"time"
)

// PruneOutbox expires undelivered notifications after the configured window.
// Receipts and deployment history remain available for replay and recovery.
func (s *Store) PruneOutbox(ctx context.Context, cutoff time.Time) error {
	if cutoff.IsZero() || cutoff.After(time.Now()) {
		return ErrInvalid
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM outbox WHERE event_id IN (
 SELECT o.event_id FROM outbox o JOIN deployments d ON d.id=o.deployment_id
 WHERE d.state IN ('succeeded','failed','canceled','abandoned')
 AND NOT EXISTS(SELECT 1 FROM deployment_audit a WHERE a.deployment_id=d.id AND a.created_at>=?)
 ORDER BY o.rowid LIMIT 5000)`, cutoff.UTC().Format("2006-01-02 15:04:05"))
	return err
}

// PruneTemplateVersions drops versions beyond the newest window for each
// template, bounding the table regardless of edit frequency. Versions are
// monotonic per template, so the retained window is always the tail by
// version number. It batches per call like the other pruners.
func (s *Store) PruneTemplateVersions(ctx context.Context, retain int) error {
	if retain < 1 {
		return ErrInvalid
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM console_template_versions WHERE rowid IN (
 SELECT v.rowid FROM console_template_versions v
 WHERE v.version NOT IN (
   SELECT newest.version FROM console_template_versions newest
   WHERE newest.template_id=v.template_id
   ORDER BY newest.version DESC LIMIT ?
 ) LIMIT 5000)`, retain)
	return err
}

// PruneAudit drops deployment and project audit rows older than the cutoff.
// It batches per call so one hourly pass catches up on large backlogs without
// holding the write lock for a full table rewrite. Outbox expiry compares
// deployment_audit recency, so callers run this with the same retention
// window as PruneOutbox.
func (s *Store) PruneAudit(ctx context.Context, cutoff time.Time) error {
	if cutoff.IsZero() || cutoff.After(time.Now()) {
		return ErrInvalid
	}
	stamp := cutoff.UTC().Format(time.DateTime)
	if _, err := s.db.ExecContext(ctx, `DELETE FROM deployment_audit WHERE rowid IN
 (SELECT rowid FROM deployment_audit WHERE created_at<? LIMIT 5000)`, stamp); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM project_audit WHERE rowid IN
 (SELECT rowid FROM project_audit WHERE created_at<? LIMIT 5000)`, stamp)
	return err
}
