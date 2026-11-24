package storage

import (
	"context"
	"time"
)

// BindAgent is a trusted administrative operation. A certificate changes only
// when the Agent generation increases.
func (s *Store) BindAgent(ctx context.Context, id string, generation int64, fingerprint [32]byte) error {
	if id == "" || generation <= 0 || fingerprint == [32]byte{} {
		return ErrInvalid
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO agent_certificates(agent,generation,fingerprint) VALUES (?,?,?)
 ON CONFLICT(agent) DO UPDATE SET generation=excluded.generation,fingerprint=excluded.fingerprint
 WHERE (excluded.generation>agent_certificates.generation AND excluded.fingerprint<>agent_certificates.fingerprint)
    OR (excluded.generation=agent_certificates.generation AND excluded.fingerprint=agent_certificates.fingerprint)`,
		id, generation, fingerprint[:])
	return expectOne(res, err, ErrConflict)
}

func (s *Store) ResolveAgent(ctx context.Context, fingerprint [32]byte) (string, int64, error) {
	var id string
	var generation int64
	err := s.db.QueryRowContext(ctx, "SELECT agent,generation FROM agent_certificates WHERE fingerprint=?", fingerprint[:]).Scan(&id, &generation)
	return id, generation, err
}

func (s *Store) ValidateAgent(ctx context.Context, id string, generation int64) error {
	var active bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_certificates WHERE agent=? AND generation=? AND NOT EXISTS (SELECT 1 FROM agent_credentials c WHERE c.agent=agent_certificates.agent AND c.generation=agent_certificates.generation AND (c.revoked=1 OR c.expires_ms<=?)))`,
		id, generation, time.Now().UnixMilli()).Scan(&active); err != nil {
		return err
	}
	if !active {
		return ErrConflict
	}
	return nil
}
