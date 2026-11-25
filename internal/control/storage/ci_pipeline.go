package storage

import (
	"context"
	"encoding/json"
	"time"

	"github.com/maxtanforge/moori/internal/core/nodeops"
)

type CIPipelineGrant struct {
	Project      string `json:"project"`
	Target       string `json:"target"`
	AllowPublish bool   `json:"allow_publish"`
}

type CIPipelineToken struct {
	ID         string            `json:"id"`
	ExpiresAt  time.Time         `json:"expires_at"`
	Revoked    bool              `json:"revoked"`
	CanPublish bool              `json:"can_publish"`
	Grants     []CIPipelineGrant `json:"grants"`
}

type CIPipelineRun struct {
	Source       string        `json:"source"`
	Kind         string        `json:"kind"`
	Release      string        `json:"release,omitempty"`
	Digest       string        `json:"digest,omitempty"`
	ID           string        `json:"id"`
	State        nodeops.State `json:"state"`
	Image        string        `json:"image"`
	Error        string        `json:"error,omitempty"`
	CreatedAt    time.Time     `json:"created_at"`
	CredentialID string        `json:"credential_id"`
}

func (s *Store) FindCIContainerTarget(ctx context.Context, session, node, name string, generation int64) (CIPipelineTarget, error) {
	var target CIPipelineTarget
	if node == "" || name == "" || generation <= 0 {
		return target, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return target, err
	}
	defer tx.Rollback()
	if _, err = platformActor(ctx, tx, session); err != nil {
		return target, err
	}
	err = tx.QueryRowContext(ctx, `SELECT t.project,b.target,b.agent,b.generation,b.name,b.repository,b.registry_id
 FROM ci_container_targets b JOIN targets t ON t.id=b.target WHERE b.agent=? AND b.generation=? AND b.name=?`, node, generation, name).
		Scan(&target.Project, &target.Target, &target.Node, &target.Generation, &target.Container, &target.Repository, &target.RegistryID)
	if err != nil {
		return target, err
	}
	target.Kind = "container"
	return target, tx.Commit()
}

func (s *Store) ListCIPipelineTargets(ctx context.Context, session, after string) (CatalogPage[CIPipelineTarget], error) {
	page := CatalogPage[CIPipelineTarget]{Items: []CIPipelineTarget{}}
	if after != "" && !webhookKey.MatchString(after) {
		return page, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return page, err
	}
	defer tx.Rollback()
	if _, err = platformActor(ctx, tx, session); err != nil {
		return page, err
	}
	const limit = 50
	rows, err := tx.QueryContext(ctx, `SELECT t.project,t.id,CASE WHEN b.target IS NULL THEN 'compose' ELSE 'container' END,
 coalesce(b.agent,a.agent,''),coalesce(b.generation,a.generation,0),coalesce(b.name,''),coalesce(b.repository,''),coalesce(b.registry_id,'')
 FROM targets t LEFT JOIN target_assignments a ON a.target=t.id LEFT JOIN ci_container_targets b ON b.target=t.id
 WHERE t.id>? ORDER BY t.id LIMIT ?`, after, limit+1)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		if len(page.Items) == limit {
			page.Next = page.Items[limit-1].Target
			break
		}
		var b CIPipelineTarget
		if err = rows.Scan(&b.Project, &b.Target, &b.Kind, &b.Node, &b.Generation, &b.Container, &b.Repository, &b.RegistryID); err != nil {
			return page, err
		}
		page.Items = append(page.Items, b)
	}
	if err = rows.Err(); err != nil {
		return page, err
	}
	return page, rows.Close()
}

func (s *Store) ListCIPipelineTokens(ctx context.Context, session, target string) ([]CIPipelineToken, error) {
	items := []CIPipelineToken{}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = platformActor(ctx, tx, session); err != nil {
		return nil, err
	}
	var exists int
	if err = tx.QueryRowContext(ctx, `SELECT 1 FROM targets WHERE id=?`, target).Scan(&exists); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT c.id,c.expires_ms,c.revoked,g.can_publish,
 (SELECT json_group_array(json_object('project',x.project,'target',x.target,'allow_publish',json(CASE WHEN x.can_publish THEN 'true' ELSE 'false' END)))
 FROM ci_credential_scopes x WHERE x.credential=c.id)
 FROM ci_credential_scopes g JOIN service_credentials c ON c.id=g.credential
 WHERE g.target=? ORDER BY c.revoked,c.expires_ms DESC,c.id LIMIT 100`, target)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var item CIPipelineToken
		var ms int64
		var grants string
		if err = rows.Scan(&item.ID, &ms, &item.Revoked, &item.CanPublish, &grants); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(grants), &item.Grants); err != nil {
			return nil, err
		}
		item.ExpiresAt = time.UnixMilli(ms).UTC()
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) IssueCIPipelineToken(ctx context.Context, session, target, id string, ttlHours int, publish bool, additional ...CIPipelineGrant) (string, error) {
	if !webhookKey.MatchString(id) || ttlHours < 1 || ttlHours > 8760 || len(additional) > 49 {
		return "", ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	actor, err := platformActor(ctx, tx, session)
	if err != nil {
		return "", err
	}
	if err = requireRecoveryComplete(ctx, tx); err != nil {
		return "", err
	}
	var project string
	var container bool
	if err = tx.QueryRowContext(ctx, `SELECT project,EXISTS(SELECT 1 FROM ci_container_targets b WHERE b.target=targets.id) FROM targets WHERE id=?`, target).Scan(&project, &container); err != nil {
		return "", err
	}
	if container && publish {
		return "", ErrInvalid
	}
	secret, err := issueServiceCredential(ctx, tx, id, project, target, time.Now().Add(time.Duration(ttlHours)*time.Hour), publish)
	if err != nil {
		return "", err
	}
	if err = addProjectAudit(ctx, tx, project, "ci_credential.created", actor, id, target); err != nil {
		return "", err
	}
	seen := map[string]bool{target: true}
	for _, scope := range additional {
		if seen[scope.Target] || !webhookKey.MatchString(scope.Project) || !webhookKey.MatchString(scope.Target) {
			return "", ErrInvalid
		}
		seen[scope.Target] = true
		res, err := tx.ExecContext(ctx, `INSERT INTO ci_credential_grants(credential,project,target,can_publish)
 SELECT ?,t.project,t.id,? FROM targets t WHERE t.project=? AND t.id=?
 AND (NOT ? OR NOT EXISTS(SELECT 1 FROM ci_container_targets b WHERE b.target=t.id))`, id, scope.AllowPublish, scope.Project, scope.Target, scope.AllowPublish)
		if err = expectOne(res, err, ErrInvalid); err != nil {
			return "", err
		}
		if err = addProjectAudit(ctx, tx, scope.Project, "ci_credential.created", actor, id, scope.Target); err != nil {
			return "", err
		}
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	return secret, nil
}

func (s *Store) RevokeCIPipelineToken(ctx context.Context, session, target, id string) error {
	if target == "" || id == "" {
		return ErrInvalid
	}
	return s.revokeServiceCredential(ctx, session, target, id)
}
