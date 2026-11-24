package storage

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

func platformActor(ctx context.Context, q queryRower, token string) (string, error) {
	hash, err := humanTokenHash(token)
	if err != nil {
		return "", err
	}
	actor, err := findHumanSession(ctx, q, hash)
	if err != nil {
		return "", err
	}
	var granted bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM platform_admins WHERE user_id=?)`, actor).Scan(&granted); err != nil {
		return "", err
	}
	if !granted {
		return "", ErrAccessDenied
	}
	return actor, nil
}

func (s *Store) HumanPlatformAdmin(ctx context.Context, token string) (bool, error) {
	_, err := platformActor(ctx, s.db, token)
	if errors.Is(err, ErrAccessDenied) {
		return false, nil
	}
	return err == nil, err
}

type AgentChoice struct {
	ID          string `json:"id"`
	Generation  int64  `json:"generation"`
	DisplayName string `json:"display_name,omitempty"`
}

type AgentChoicePage struct {
	CatalogPage[AgentChoice]
	LocalDisplayName string `json:"local_display_name,omitempty"`
}

func (s *Store) ListHumanAgents(ctx context.Context, token, after string) (AgentChoicePage, error) {
	page := AgentChoicePage{CatalogPage: CatalogPage[AgentChoice]{Items: []AgentChoice{}}}
	if len(after) > 128 {
		return page, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return page, err
	}
	defer tx.Rollback()
	if _, err := platformActor(ctx, tx, token); err != nil {
		return page, err
	}
	page.LocalDisplayName, err = nodeDisplayName(ctx, tx, LocalExecutorID)
	if err != nil {
		return page, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT c.agent,c.generation,COALESCE(n.display_name,'')
 FROM agent_credentials c JOIN agent_certificates a ON a.agent=c.agent AND a.generation=c.generation AND a.fingerprint=c.token_hash
 LEFT JOIN node_display_names n ON n.agent=c.agent
 WHERE c.revoked=0 AND c.expires_ms>? AND c.agent>? ORDER BY c.agent LIMIT 51`, time.Now().UnixMilli(), after)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		var a AgentChoice
		if err := rows.Scan(&a.ID, &a.Generation, &a.DisplayName); err != nil {
			return page, err
		}
		if len(page.Items) == 50 {
			page.Next = page.Items[49].ID
			break
		}
		page.Items = append(page.Items, a)
	}
	if err := rows.Err(); err != nil {
		return page, err
	}
	return page, nil
}

type TargetSetup struct {
	Project    string `json:"project"`
	Target     string `json:"target"`
	Agent      string `json:"agent"`
	Generation int64  `json:"generation"`
	Local      bool   `json:"local"`
}

// ProvisionHumanTarget binds a new target to a registered execution identity.
// An identical retry is safe if the assignment is still valid.
func (s *Store) ProvisionHumanTarget(ctx context.Context, session string, in TargetSetup) error {
	if !enrollmentAgentID.MatchString(in.Project) || !enrollmentAgentID.MatchString(in.Target) || (!in.Local && !enrollmentAgentID.MatchString(in.Agent)) {
		return ErrInvalid
	}
	if in.Local {
		if in.Agent != "" || in.Generation != 0 {
			return ErrInvalid
		}
		in.Agent = LocalExecutorID
		in.Generation = 1
	} else if in.Generation <= 0 {
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
	var valid bool
	if in.Local {
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_certificates WHERE agent=? AND generation=1)`, LocalExecutorID).Scan(&valid)
	} else {
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_credentials c JOIN agent_certificates a ON a.agent=c.agent AND a.generation=c.generation AND a.fingerprint=c.token_hash WHERE c.agent=? AND c.generation=? AND c.revoked=0 AND c.expires_ms>?)`, in.Agent, in.Generation, time.Now().UnixMilli()).Scan(&valid)
	}
	if err != nil {
		return err
	}
	var project, agent string
	var generation int64
	err = tx.QueryRowContext(ctx, `SELECT t.project,a.agent,a.generation FROM targets t JOIN target_assignments a ON a.target=t.id WHERE t.id=?`, in.Target).Scan(&project, &agent, &generation)
	if err == nil {
		if project != in.Project || agent != in.Agent || generation != in.Generation || !valid {
			return ErrConflict
		}
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM targets WHERE id=?)`, in.Target).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return ErrConflict
	}
	if !valid {
		return ErrConflict
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO targets(id,project) VALUES(?,?)`, in.Target, in.Project); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO target_assignments(target,agent,generation) VALUES(?,?,?)`, in.Target, in.Agent, in.Generation); err != nil {
		return err
	}
	if err := addProjectAudit(ctx, tx, in.Project, "target.onboarded", actor, in.Target, in.Agent); err != nil {
		return err
	}
	return tx.Commit()
}
