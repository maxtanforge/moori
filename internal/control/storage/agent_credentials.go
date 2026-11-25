package storage

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/maxtanforge/moori/internal/core/agenttoken"
)

type IssuedAgentToken struct {
	ID         string `json:"id"`
	Generation int64  `json:"generation"`
	Token      string `json:"token"`
}

const browserAgentTokenLifetime = 365 * 24 * time.Hour
const agentTokenRetryWindow = 5 * time.Minute

// CreateHumanAgentToken enrolls a new Agent independently of deployment targets.
// A retry with the original key can recover a lost response without rotation.
func (s *Store) CreateHumanAgentToken(ctx context.Context, session, id, requestKey string) (IssuedAgentToken, error) {
	var issued IssuedAgentToken
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return issued, err
	}
	defer tx.Rollback()
	actor, err := platformActor(ctx, tx, session)
	if err != nil {
		return issued, err
	}
	if !enrollmentAgentID.MatchString(id) {
		return issued, ErrInvalid
	}
	if err := requireRecoveryComplete(ctx, tx); err != nil {
		return issued, err
	}
	// A consumed request can retry the same live issuance, but cannot enroll a
	// replacement node after its original credential was retired or rotated.
	var generation int64
	err = tx.QueryRowContext(ctx, "SELECT COALESCE((SELECT generation FROM agent_certificates WHERE agent=?),"+nextAgentGeneration+")", id, id).Scan(&generation)
	if err != nil {
		return issued, err
	}
	token, hash, err := s.agentTokenForRequest(session, id, generation, requestKey)
	if err != nil {
		return issued, err
	}
	requestHash := sha256.Sum256([]byte(session + "\x00" + id + "\x00" + strings.ToLower(requestKey)))
	if requestKey != "" {
		var consumed bool
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_identity_history WHERE request_hash=? AND generation<>?)`, requestHash[:], generation).Scan(&consumed)
		if err != nil {
			return issued, err
		}
		if consumed {
			return issued, ErrConflict
		}
	}
	now := time.Now()
	res, err := tx.ExecContext(ctx, `INSERT INTO agent_certificates(agent,generation,fingerprint) VALUES(?,?,?) ON CONFLICT(agent) DO NOTHING`, id, generation, hash[:])
	if err := expectOne(res, err, ErrConflict); err != nil {
		if requestKey == "" || !errors.Is(err, ErrConflict) {
			return issued, err
		}
		var match bool
		err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_credentials c
 JOIN agent_certificates a ON a.agent=c.agent AND a.generation=c.generation AND a.fingerprint=c.token_hash
 WHERE c.agent=? AND c.token_hash=? AND c.generation=? AND c.revoked=0 AND c.expires_ms>?)`,
			id, hash[:], generation, now.Add(browserAgentTokenLifetime-agentTokenRetryWindow).UnixMilli()).Scan(&match)
		if err != nil {
			return issued, err
		}
		if !match {
			return issued, ErrConflict
		}
		return IssuedAgentToken{ID: id, Generation: generation, Token: token}, tx.Commit()
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO agent_credentials(agent,generation,token_hash,expires_ms) VALUES(?,?,?,?)", id, generation, hash[:], now.Add(browserAgentTokenLifetime).UnixMilli()); err != nil {
		return issued, err
	}
	if requestKey != "" {
		if _, err = tx.ExecContext(ctx, "INSERT INTO agent_identity_history(agent,generation,request_hash) VALUES(?,?,?)", id, generation, requestHash[:]); err != nil {
			return issued, err
		}
	}
	if err := addProjectAudit(ctx, tx, "", "agent.token_created", actor, id, ""); err != nil {
		return issued, err
	}
	if err := tx.Commit(); err != nil {
		return issued, err
	}
	return IssuedAgentToken{ID: id, Generation: generation, Token: token}, nil
}

// The server contributes a process-private key, so an idempotency key alone
// cannot derive a bearer token. The binding includes the requesting session.
func (s *Store) agentTokenForRequest(session, id string, generation int64, requestKey string) (string, [32]byte, error) {
	if requestKey == "" {
		return generateAgentToken()
	}
	if len(requestKey) != 64 {
		return "", [32]byte{}, ErrInvalid
	}
	raw, err := hex.DecodeString(requestKey)
	if err != nil {
		return "", [32]byte{}, ErrInvalid
	}
	mac := hmac.New(sha256.New, s.agentIssueKey[:])
	mac.Write([]byte(session + "\x00" + id + "\x00"))
	if generation > 1 {
		mac.Write([]byte(strconv.FormatInt(generation, 10) + "\x00"))
	}
	mac.Write(raw)
	token := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return token, sha256.Sum256([]byte(token)), nil
}

func generateAgentToken() (string, [32]byte, error) {
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", [32]byte{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(random[:])
	return token, sha256.Sum256([]byte(token)), nil
}

// IssueAgentCredential rotates the identity generation atomically with its token.
// The first offline-restore reissue preserves the inventoried generation; all
// subsequent issuance rotates it. Only the SHA-256 digest is persisted; callers must securely deliver the token.
func (s *Store) IssueAgentCredential(ctx context.Context, agent string, lifetime time.Duration) (string, int64, error) {
	if !enrollmentAgentID.MatchString(agent) || lifetime <= 0 || lifetime > 10*365*24*time.Hour {
		return "", 0, ErrInvalid
	}
	token, hash, err := generateAgentToken()
	if err != nil {
		return "", 0, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", 0, err
	}
	defer tx.Rollback()
	var generation int64
	err = tx.QueryRowContext(ctx, `INSERT INTO agent_certificates(agent,generation,fingerprint) VALUES (?,`+nextAgentGeneration+`,?) ON CONFLICT(agent) DO UPDATE SET generation=CASE WHEN EXISTS (
 SELECT 1 FROM agent_credentials c WHERE c.agent=agent_certificates.agent
 AND c.generation=agent_certificates.generation AND c.token_hash=agent_certificates.fingerprint
 AND c.recovery_pending=1 AND c.revoked=1
 AND EXISTS (SELECT 1 FROM recovery_state WHERE singleton=1 AND epoch<>'')
 ) THEN agent_certificates.generation ELSE agent_certificates.generation+1 END,
 fingerprint=excluded.fingerprint RETURNING generation`, agent, agent, hash[:]).Scan(&generation)
	if err != nil {
		return "", 0, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO agent_credentials(agent,generation,token_hash,expires_ms) VALUES (?,?,?,?) ON CONFLICT(agent) DO UPDATE SET generation=excluded.generation,token_hash=excluded.token_hash,expires_ms=excluded.expires_ms,revoked=0,recovery_pending=0`, agent, generation, hash[:], time.Now().Add(lifetime).UnixMilli())
	if err != nil {
		return "", 0, err
	}
	if err = tx.Commit(); err != nil {
		return "", 0, err
	}
	s.cancelAgentSessions(agent, generation-1)
	return token, generation, nil
}
func (s *Store) RevokeAgentCredential(ctx context.Context, agent string) error {
	var generation int64
	err := s.db.QueryRowContext(ctx, `UPDATE agent_credentials SET revoked=1 WHERE agent=? RETURNING generation`, agent).Scan(&generation)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrForbidden
	}
	if err == nil {
		s.cancelAgentSessions(agent, generation)
	}
	return err
}
func (s *Store) AuthenticateAgentCredential(ctx context.Context, token string) (string, int64, error) {
	if !agenttoken.Valid(token) {
		return "", 0, ErrForbidden
	}
	hash := sha256.Sum256([]byte(token))
	var agent string
	var generation int64
	err := s.db.QueryRowContext(ctx, `SELECT c.agent,c.generation FROM agent_credentials c JOIN agent_certificates a ON a.agent=c.agent AND a.generation=c.generation AND a.fingerprint=c.token_hash WHERE c.token_hash=? AND c.revoked=0 AND c.expires_ms>?`, hash[:], time.Now().UnixMilli()).Scan(&agent, &generation)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrForbidden
	}
	return agent, generation, err
}
