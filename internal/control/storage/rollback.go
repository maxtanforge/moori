package storage

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/maxtanforge/moori/internal/core/deployment"
	"github.com/maxtanforge/moori/internal/core/release"
)

type RollbackRequest struct {
	ID, Source, Key, Operator, Note string
}

func (s *Store) RequestRollback(ctx context.Context, r RollbackRequest) (string, error) {
	r.Operator = strings.TrimSpace(r.Operator)
	if r.Operator == "" || len(r.Operator) > 128 {
		return "", ErrInvalid
	}
	return s.requestRollback(ctx, r, nil, nil)
}

func (s *Store) RequestServiceRollback(ctx context.Context, token string, r RollbackRequest) (string, error) {
	hash, err := credentialHash(token)
	if err != nil {
		return "", err
	}
	return s.requestRollback(ctx, r, &hash, nil)
}

func (s *Store) RequestHumanRollback(ctx context.Context, token, project string, r RollbackRequest) (string, error) {
	session, err := newProjectSession(project, token)
	if err != nil {
		return "", err
	}
	if _, err := session.authorize(ctx, s.db, true); err != nil {
		return "", err
	}
	return s.requestRollback(ctx, r, nil, &session)
}

func (s *Store) requestRollback(ctx context.Context, r RollbackRequest, hash *[32]byte, session *projectSession) (string, error) {
	r.Note = strings.TrimSpace(r.Note)
	if r.ID == "" || r.Source == "" || r.Key == "" || r.Note == "" || len(r.Note) > 2048 {
		return "", ErrInvalid
	}
	a := Admission{ID: r.ID, Caller: r.Operator, Key: r.Key, rollbackOf: r.Source, rollbackReason: r.Note, session: session}
	query := `SELECT d.project,d.target,d.release_id,d.digest,d.state,p.document
 FROM deployments d JOIN snapshots p ON p.deployment_id=d.id WHERE d.id=?`
	args := []any{r.Source}
	if session != nil {
		query += ` AND d.project=?`
		args = append(args, session.project)
	}
	if hash != nil {
		query += ` AND EXISTS(SELECT 1 FROM service_credentials c WHERE c.id=d.credential_id
 AND c.token_hash=? AND c.revoked=0 AND c.expires_ms>?)`
		args = append(args, hash[:], time.Now().UnixMilli())
	}
	var document, digest string
	var state deployment.State
	if err := s.db.QueryRowContext(ctx, query, args...).Scan(&a.Project, &a.Target, &a.Release, &digest, &state, &document); err != nil {
		return "", err
	}
	if state != deployment.Succeeded {
		return "", ErrState
	}
	snapshot, err := release.ParseSnapshot(ctx, document, digest)
	if err != nil {
		return "", err
	}
	return s.admitSnapshot(ctx, a, snapshot, hash, "")
}

func validateRollbackSource(ctx context.Context, tx *sql.Tx, a Admission, snapshot release.Snapshot, credential any) error {
	var state deployment.State
	var digest string
	var owner sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT state,digest,credential_id FROM deployments
 WHERE id=? AND project=? AND target=? AND release_id=?`, a.rollbackOf, a.Project, a.Target, a.Release).Scan(&state, &digest, &owner); err != nil {
		return err
	}
	if state != deployment.Succeeded {
		return ErrState
	}
	if digest != snapshot.Digest() {
		return ErrConflict
	}
	if id, ok := credential.(string); ok && (!owner.Valid || owner.String != id) {
		return ErrForbidden
	}
	return nil
}
