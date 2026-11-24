package storage

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"errors"
	"regexp"
	"time"
)

var enrollmentAgentID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)

func (s *Store) IssueEnrollment(ctx context.Context, agent string, ttl time.Duration) (string, error) {
	if !enrollmentAgentID.MatchString(agent) || ttl <= 0 || ttl > time.Hour {
		return "", ErrInvalid
	}
	token := "moori_enroll_" + rand.Text()
	hash := sha256.Sum256([]byte(token))
	now := time.Now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM agent_enrollment_tokens WHERE expires_ms<=?`, now.UnixMilli()); err != nil {
		return "", err
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO agent_enrollment_tokens(token_hash,agent,expires_ms)
 SELECT ?,?,? WHERE NOT EXISTS (SELECT 1 FROM agent_certificates WHERE agent=?)
 ON CONFLICT(agent) DO UPDATE SET token_hash=excluded.token_hash,expires_ms=excluded.expires_ms`,
		hash[:], agent, now.Add(ttl).UnixMilli(), agent)
	if err := expectOne(res, err, ErrConflict); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return token, nil
}

func (s *Store) CompleteEnrollment(ctx context.Context, token string, csr [32]byte, now time.Time, sign func(string, int64, string) ([]byte, error)) ([]byte, error) {
	hash := sha256.Sum256([]byte(token))
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var agent string
	var previousCSR, certificate []byte
	err = tx.QueryRowContext(ctx, `SELECT agent,csr_hash,certificate FROM agent_enrollment_tokens WHERE token_hash=? AND expires_ms>?`,
		hash[:], now.UnixMilli()).Scan(&agent, &previousCSR, &certificate)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrForbidden
	}
	if err != nil {
		return nil, err
	}
	if certificate != nil {
		if !bytes.Equal(previousCSR, csr[:]) {
			return nil, ErrForbidden
		}
		fingerprint := sha256.Sum256(certificate)
		var active bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_certificates WHERE agent=? AND generation=1 AND fingerprint=?)`, agent, fingerprint[:]).Scan(&active); err != nil {
			return nil, err
		}
		if !active {
			return nil, ErrForbidden
		}
		return certificate, tx.Commit()
	}
	lineage := rand.Text()
	certificate, err = sign(agent, 1, lineage)
	if err != nil {
		return nil, err
	}
	fingerprint := sha256.Sum256(certificate)
	res, err := tx.ExecContext(ctx, `INSERT INTO agent_certificates(agent,generation,fingerprint) VALUES (?,1,?) ON CONFLICT(agent) DO NOTHING`, agent, fingerprint[:])
	if err := expectOne(res, err, ErrConflict); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO managed_agent_identities VALUES (?,1,?)`, agent, lineage); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_enrollment_tokens SET csr_hash=?,certificate=? WHERE token_hash=?`, csr[:], certificate, hash[:]); err != nil {
		return nil, err
	}
	return certificate, tx.Commit()
}

func (s *Store) ValidateManagedAgent(ctx context.Context, agent string, generation int64, lineage string) error {
	return validateManagedAgent(ctx, s.db, agent, generation, lineage)
}

func validateManagedAgent(ctx context.Context, q queryRower, agent string, generation int64, lineage string) error {
	var active bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM managed_agent_identities m
 JOIN agent_certificates a ON a.agent=m.agent AND a.generation=m.generation
 WHERE m.agent=? AND m.generation=? AND m.lineage=?)`, agent, generation, lineage).Scan(&active)
	if err != nil {
		return err
	}
	if !active {
		return ErrForbidden
	}
	return nil
}

func (s *Store) RenewAgent(ctx context.Context, agent string, generation int64, lineage string, old, csr [32]byte, now, expiry time.Time, sign func() ([]byte, error)) ([]byte, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := validateManagedAgent(ctx, tx, agent, generation, lineage); err != nil {
		return nil, err
	}
	var previousCSR, certificate []byte
	err = tx.QueryRowContext(ctx, `SELECT csr_hash,certificate FROM agent_certificate_renewals WHERE old_fingerprint=?`, old[:]).Scan(&previousCSR, &certificate)
	if err == nil {
		if !bytes.Equal(previousCSR, csr[:]) {
			return nil, ErrConflict
		}
		return certificate, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM agent_certificate_renewals WHERE expires_ms<=?`, now.UnixMilli()); err != nil {
		return nil, err
	}
	certificate, err = sign()
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_certificate_renewals VALUES (?,?,?,?)`, old[:], expiry.UnixMilli(), csr[:], certificate); err != nil {
		return nil, err
	}
	return certificate, tx.Commit()
}
