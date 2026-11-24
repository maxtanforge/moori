package storage

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const containerRunsQuery = `SELECT q.id,q.state,q.error,q.created_ms,
 coalesce(c.image,w.image,json_extract(q.request,'$.history_image'),json_extract(q.result,'$.container.image'),''),
 coalesce(c.credential,w.webhook,''),
 CASE WHEN c.operation IS NOT NULL THEN 'ci' WHEN w.operation IS NOT NULL THEN 'webhook' ELSE 'manual' END
 FROM node_operations q LEFT JOIN ci_container_deliveries c ON c.operation=q.id
 LEFT JOIN container_webhook_deliveries w ON w.operation=q.id
 WHERE q.agent=? AND q.generation=? AND q.action IN ('container.create','container.replace','container.release')
 AND %s
 AND (?=0 OR (q.created_ms,q.id)<(?,?)) ORDER BY q.created_ms DESC,q.id DESC LIMIT ?`

const containerRunsByName = `(q.history_target=? OR (q.action='container.replace' AND json_extract(q.result,'$.container.name')=?))`

const containerRunsByInstance = `json_extract(q.result,'$.container.full_id')=?`

type DeploymentRunPage struct {
	Items   []CIPipelineRun `json:"items"`
	Next    string          `json:"next,omitempty"`
	Project string          `json:"project,omitempty"`
	Target  string          `json:"target,omitempty"`
}

func (s *Store) PageCIPipelineRuns(ctx context.Context, session, target, after string, limit int) (DeploymentRunPage, error) {
	page := DeploymentRunPage{Items: []CIPipelineRun{}}
	if target == "" || len(target) > 128 || len(after) > 128 || limit < 1 || limit > 50 {
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
	var agent sql.NullString
	var generation int64
	var container string
	if err = tx.QueryRowContext(ctx, `SELECT b.agent,coalesce(b.generation,0),coalesce(b.name,'')
 FROM targets t LEFT JOIN ci_container_targets b ON b.target=t.id WHERE t.id=?`, target).Scan(&agent, &generation, &container); err != nil {
		return page, err
	}
	if agent.Valid {
		page, err = pageContainerDeploymentRuns(ctx, tx, agent.String, generation, container, "", after, limit, time.Now())
	} else {
		page, err = pageComposeDeploymentRuns(ctx, tx, target, after, limit)
	}
	if err != nil {
		return page, err
	}
	return page, tx.Commit()
}

func (s *Store) PageManagedComposeDeploymentRuns(ctx context.Context, session, agent, name, after string, limit int) (DeploymentRunPage, error) {
	if agent == "" || len(agent) > 128 || len(after) > 128 || limit < 1 || limit > 50 {
		return DeploymentRunPage{}, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return DeploymentRunPage{}, err
	}
	defer tx.Rollback()
	if _, err = platformActor(ctx, tx, session); err != nil {
		return DeploymentRunPage{}, err
	}
	source, err := findManagedComposeTarget(ctx, tx, agent, name)
	if err != nil {
		return DeploymentRunPage{}, err
	}
	page, err := pageComposeDeploymentRuns(ctx, tx, source.Target, after, limit)
	if err != nil {
		return page, err
	}
	page.Project, page.Target = source.Project, source.Target
	return page, tx.Commit()
}

func pageComposeDeploymentRuns(ctx context.Context, tx *sql.Tx, target, after string, limit int) (DeploymentRunPage, error) {
	page := DeploymentRunPage{Items: []CIPipelineRun{}}
	// The first audit event can age out independently of the deployment. Use
	// the boundary deployment ID to resolve its insertion order when paging.
	rows, err := tx.QueryContext(ctx, `WITH recent AS (
 SELECT id,rowid AS position FROM deployments WHERE target=?
 AND (?='' OR rowid<(SELECT rowid FROM deployments WHERE target=? AND id=?))
 ORDER BY rowid DESC LIMIT ?
)
 SELECT d.id,d.state,d.release_id,d.digest,d.caller,d.created_at,d.last_reason,
 CASE WHEN d.rollback_of IS NOT NULL THEN 'rollback' WHEN d.caller GLOB 'integration:*' THEN 'webhook'
 WHEN d.credential_id IS NOT NULL THEN 'ci' ELSE 'manual' END
 FROM recent JOIN deployments d ON d.id=recent.id
 ORDER BY recent.position DESC`, target, after, target, after, limit+1)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		if len(page.Items) == limit {
			page.Next = page.Items[limit-1].ID
			break
		}
		item := CIPipelineRun{Kind: "compose"}
		var created string
		if err := rows.Scan(&item.ID, &item.State, &item.Release, &item.Digest, &item.CredentialID, &created, &item.Error, &item.Source); err != nil {
			return page, err
		}
		item.CreatedAt, _ = time.Parse(time.DateTime, created)
		page.Items = append(page.Items, item)
	}
	return page, rows.Err()
}

func (s *Store) PageContainerDeploymentRuns(ctx context.Context, session, agent string, generation int64, name, containerID, after string, limit int) (DeploymentRunPage, error) {
	if agent == "" || len(agent) > 128 || generation <= 0 || name == "" || len(name) > 128 ||
		len(containerID) > 64 || limit < 1 || limit > 50 {
		return DeploymentRunPage{}, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return DeploymentRunPage{}, err
	}
	defer tx.Rollback()
	if _, err := platformActor(ctx, tx, session); err != nil {
		return DeploymentRunPage{}, err
	}
	page, err := pageContainerDeploymentRuns(ctx, tx, agent, generation, name, containerID, after, limit, time.Now())
	if err != nil {
		return page, err
	}
	return page, tx.Commit()
}

func pageContainerDeploymentRuns(ctx context.Context, tx *sql.Tx, agent string, generation int64, name, containerID, after string, limit int, now time.Time) (DeploymentRunPage, error) {
	page := DeploymentRunPage{Items: []CIPipelineRun{}}
	var cursorTime int64
	var cursorID string
	if after != "" {
		timestamp, id, ok := strings.Cut(after, ".")
		parsed, err := strconv.ParseInt(timestamp, 10, 64)
		if !ok || err != nil || parsed <= 0 || len(id) != 32 {
			return page, ErrInvalid
		}
		cursorTime, cursorID = parsed, id
	}
	if err := retireExpiredNodeOperations(ctx, tx, agent, now); err != nil {
		return page, err
	}
	// The name view follows the managed container name: completed
	// create/replace operations adopt the resulting name, so a history that
	// was renamed into this name still shows up. The instance view matches
	// the exact container ID recorded in each operation result, so history
	// stays attached to the container it ran against even after a replacement
	// reuses the name for a new instance. Operations without a result
	// container (failures before creation) cannot be attributed to an
	// instance and only appear in the name view.
	scope := containerRunsByName
	args := []any{agent, generation, name, name, cursorTime, cursorTime, cursorID, limit + 1}
	if containerID != "" {
		scope = containerRunsByInstance
		args = []any{agent, generation, containerID, cursorTime, cursorTime, cursorID, limit + 1}
	}
	rows, err := tx.QueryContext(ctx, fmt.Sprintf(containerRunsQuery, scope), args...)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		if len(page.Items) == limit {
			last := page.Items[limit-1]
			page.Next = strconv.FormatInt(last.CreatedAt.UnixMilli(), 10) + "." + last.ID
			break
		}
		item := CIPipelineRun{Kind: "container"}
		var ms int64
		if err := rows.Scan(&item.ID, &item.State, &item.Error, &ms, &item.Image, &item.CredentialID, &item.Source); err != nil {
			return page, err
		}
		item.CreatedAt = time.UnixMilli(ms).UTC()
		page.Items = append(page.Items, item)
	}
	return page, rows.Err()
}
