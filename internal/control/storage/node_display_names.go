package storage

import (
	"context"
	"database/sql"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// NodeDisplayName returns presentation metadata. Callers must authorize node access.
func (s *Store) NodeDisplayName(ctx context.Context, id string) (string, error) {
	return nodeDisplayName(ctx, s.db, id)
}

func nodeDisplayName(ctx context.Context, q queryRower, id string) (string, error) {
	var name string
	err := q.QueryRowContext(ctx, "SELECT COALESCE((SELECT display_name FROM node_display_names WHERE agent=?),'')", id).Scan(&name)
	return name, err
}

// SetHumanNodeDisplayName changes a display label for the stable node ID.
// The label is independent of the reported hostname and credential generation.
func (s *Store) SetHumanNodeDisplayName(ctx context.Context, token, id, name string) error {
	name = strings.TrimSpace(name)
	if id != LocalExecutorID && !enrollmentAgentID.MatchString(id) ||
		utf8.RuneCountInString(name) > 64 || strings.ContainsFunc(name, func(r rune) bool {
		return unicode.IsControl(r) || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r)
	}) {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	actor, err := platformActor(ctx, tx, token)
	if err != nil {
		return err
	}
	if id != LocalExecutorID {
		var registered bool
		err := tx.QueryRowContext(ctx, `SELECT EXISTS(
 SELECT 1 FROM agent_credentials c JOIN agent_certificates a
 ON a.agent=c.agent AND a.generation=c.generation AND a.fingerprint=c.token_hash
 WHERE c.agent=? AND c.revoked=0 AND c.expires_ms>?)`, id, time.Now().UnixMilli()).Scan(&registered)
		if err != nil {
			return err
		}
		if !registered {
			return sql.ErrNoRows
		}
	}
	var result sql.Result
	if name == "" {
		result, err = tx.ExecContext(ctx, "DELETE FROM node_display_names WHERE agent=?", id)
	} else {
		result, err = tx.ExecContext(ctx, `INSERT INTO node_display_names(agent,display_name) VALUES (?,?)
 ON CONFLICT(agent) DO UPDATE SET display_name=excluded.display_name
 WHERE node_display_names.display_name<>excluded.display_name`, id, name)
	}
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed > 0 {
		if err := addProjectAudit(ctx, tx, "", "node.display_name_changed", actor, id, name); err != nil {
			return err
		}
	}
	return tx.Commit()
}
