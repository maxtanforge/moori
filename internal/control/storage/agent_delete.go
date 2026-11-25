package storage

import (
	"context"
	"strconv"
	"time"
)

// Use the last retired generation even when the current identity row is gone.
const nextAgentGeneration = `(SELECT coalesce(max(generation),0)+1 FROM agent_identity_history WHERE agent=? AND retired_fingerprint IS NOT NULL)`

type agentSession struct {
	agent      string
	generation int64
	cancel     context.CancelFunc
}

// AgentContext tracks a live relay or channel for this credential generation.
// Register and validate under the same lock as post-commit cancellation, so
// connections authenticated just before deletion cannot miss the notification.
func (s *Store) AgentContext(ctx context.Context, agent string, generation int64) (context.Context, context.CancelFunc, error) {
	s.agentSessionsMu.Lock()
	defer s.agentSessionsMu.Unlock()
	if err := s.ValidateAgent(ctx, agent, generation); err != nil {
		return nil, nil, err
	}
	live, cancel := context.WithCancel(ctx)
	session := &agentSession{agent, generation, cancel}
	s.agentSessions[session] = struct{}{}
	return live, func() {
		cancel()
		s.agentSessionsMu.Lock()
		delete(s.agentSessions, session)
		s.agentSessionsMu.Unlock()
	}, nil
}

func (s *Store) cancelAgentSessions(agent string, through int64) {
	s.agentSessionsMu.Lock()
	defer s.agentSessionsMu.Unlock()
	for session := range s.agentSessions {
		if session.agent == agent && session.generation <= through {
			session.cancel()
			delete(s.agentSessions, session)
		}
	}
}

func (s *Store) DeleteHumanAgent(ctx context.Context, session, agent string, generation int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	actor, err := platformActor(ctx, tx, session)
	if err != nil {
		return err
	}
	if agent == LocalExecutorID || !enrollmentAgentID.MatchString(agent) || generation <= 0 {
		return ErrInvalid
	}
	var current int64
	if err := tx.QueryRowContext(ctx, "SELECT generation FROM agent_certificates WHERE agent=?", agent).Scan(&current); err != nil {
		return err
	}
	if generation != current {
		return ErrConflict
	}
	if err := requireRecoveryComplete(ctx, tx); err != nil {
		return err
	}
	now := time.Now()
	if err := retireExpiredNodeOperations(ctx, tx, agent, now); err != nil {
		return err
	}
	var busy bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM node_operations WHERE agent=? AND (state IN ('queued','running') OR (restore_fenced=1 AND expires_ms>?)))
 OR EXISTS(SELECT 1 FROM target_slots l JOIN deployments d ON d.id=l.deployment_id WHERE d.agent=?)
 OR EXISTS(SELECT 1 FROM target_assignments a JOIN deployments d ON d.target=a.target
 WHERE a.agent=? AND (d.state='queued' OR EXISTS(SELECT 1 FROM target_slots l WHERE l.deployment_id=d.id)))`,
		agent, now.UnixMilli(), agent, agent).Scan(&busy); err != nil {
		return err
	}
	if busy {
		return ErrTargetBusy
	}
	if err := tx.QueryRowContext(ctx, recoveryObservations+`
 SELECT EXISTS(SELECT 1 FROM observed o WHERE o.agent=? AND `+latestActiveRecoveryObservation+`)`, agent).Scan(&busy); err != nil {
		return err
	}
	if busy {
		return ErrTargetBusy
	}

	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_identity_history(agent,generation,retired_fingerprint)
 SELECT agent,generation,fingerprint FROM agent_certificates WHERE agent=?
 ON CONFLICT(agent,generation) DO UPDATE SET retired_fingerprint=excluded.retired_fingerprint`, agent); err != nil {
		return err
	}
	// A restored target's released assignment is required by ReassignTarget.
	// Its old generation stays fenced by canDispatchTarget until reassignment.
	for _, stmt := range []string{
		`DELETE FROM target_assignments WHERE agent=? AND NOT EXISTS(SELECT 1 FROM targets t JOIN recovery_state rs ON rs.epoch=t.restore_epoch WHERE t.id=target_assignments.target)`,
		`UPDATE container_webhooks SET revoked=1 WHERE agent=? AND revoked=0`,
		`DELETE FROM registry_bindings WHERE agent=?`,
		`DELETE FROM managed_agent_identities WHERE agent=?`,
		`DELETE FROM agent_enrollment_tokens WHERE agent=?`,
		`DELETE FROM node_operation_capabilities WHERE agent=?`,
		`DELETE FROM node_display_names WHERE agent=?`,
		`DELETE FROM agent_credentials WHERE agent=?`,
		`DELETE FROM agent_certificates WHERE agent=?`,
	} {
		if _, err := tx.ExecContext(ctx, stmt, agent); err != nil {
			return err
		}
	}
	if err := addProjectAudit(ctx, tx, "", "node.deleted", actor, agent, "generation "+strconv.FormatInt(generation, 10)); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.cancelAgentSessions(agent, generation)
	return nil
}
