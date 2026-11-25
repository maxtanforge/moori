package storage

import (
	"context"
	"time"
)

func (s *Store) AgentGeneration(ctx context.Context, agent string) (int64, error) {
	var generation int64
	err := s.db.QueryRowContext(ctx, "SELECT generation FROM agent_certificates WHERE agent=?", agent).Scan(&generation)
	return generation, err
}

type AgentGeneration struct {
	ID         string
	Generation int64
}

func (s *Store) ListAgentGenerations(ctx context.Context, after string) ([]AgentGeneration, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT a.agent,a.generation FROM agent_certificates a WHERE a.agent>?
 AND NOT EXISTS (SELECT 1 FROM agent_credentials c WHERE c.agent=a.agent AND c.generation=a.generation
                 AND (c.revoked=1 OR c.expires_ms<=?))
 ORDER BY a.agent LIMIT 64`, after, time.Now().UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []AgentGeneration
	for rows.Next() {
		var item AgentGeneration
		if err := rows.Scan(&item.ID, &item.Generation); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

// ReadHumanTargetAgent binds a project reader to the target's current Agent.
func (s *Store) ReadHumanTargetAgent(ctx context.Context, token, project, target string) (string, int64, error) {
	hash, err := humanTokenHash(token)
	if err != nil {
		return "", 0, err
	}
	var agent string
	var generation int64
	err = s.db.QueryRowContext(ctx, `SELECT a.agent,a.generation FROM targets t
 JOIN human_sessions u ON u.token_hash=? AND u.expires_ms>?
 JOIN target_assignments a ON a.target=t.id
 JOIN agent_certificates c ON c.agent=a.agent AND c.generation=a.generation
 LEFT JOIN project_members m ON m.project=t.project AND m.user_id=u.user_id
 WHERE t.project=? AND t.id=? AND `+projectReadGrant, hash[:], time.Now().UnixMilli(), project, target).
		Scan(&agent, &generation)
	return agent, generation, err
}
