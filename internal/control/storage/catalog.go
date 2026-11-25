package storage

import (
	"context"
	"database/sql"
	"time"
)

type ProjectSummary struct {
	ID   string `json:"id"`
	Role string `json:"role"`
}

type CatalogItem struct {
	ID      string `json:"id"`
	Target  string `json:"target,omitempty"`
	Release string `json:"release,omitempty"`
	State   string `json:"state,omitempty"`
	Digest  string `json:"digest,omitempty"`
}

type CatalogPage[T any] struct {
	Items []T    `json:"items"`
	Next  string `json:"next,omitempty"`
}

func (s *Store) ListHumanProjects(ctx context.Context, token, after string) (CatalogPage[ProjectSummary], error) {
	hash, err := humanTokenHash(token)
	if err != nil {
		return CatalogPage[ProjectSummary]{}, err
	}
	if len(after) > 128 {
		return CatalogPage[ProjectSummary]{}, ErrInvalid
	}
	rows, err := s.db.QueryContext(ctx, `SELECT p.project,CASE WHEN `+platformMembership+` THEN 'platform_admin' ELSE m.role END
 FROM (SELECT DISTINCT project FROM targets) p
 JOIN human_sessions u ON u.token_hash=? AND u.expires_ms>?
 LEFT JOIN project_members m ON m.project=p.project AND m.user_id=u.user_id
 WHERE p.project>? AND `+projectReadGrant+` ORDER BY p.project LIMIT 51`, hash[:], time.Now().UnixMilli(), after)
	if err != nil {
		return CatalogPage[ProjectSummary]{}, err
	}
	defer rows.Close()
	page := CatalogPage[ProjectSummary]{Items: []ProjectSummary{}}
	for rows.Next() {
		var item ProjectSummary
		if err := rows.Scan(&item.ID, &item.Role); err != nil {
			return page, err
		}
		if len(page.Items) == 50 {
			page.Next = page.Items[49].ID
			break
		}
		page.Items = append(page.Items, item)
	}
	return page, rows.Err()
}

// ListHumanCatalog holds authorization and enumeration in one transaction so
// membership changes cannot expose a different project's resource catalog.
func (s *Store) ListHumanCatalog(ctx context.Context, token, project, kind, target, after string) (CatalogPage[CatalogItem], error) {
	page := CatalogPage[CatalogItem]{Items: []CatalogItem{}}
	hash, err := humanTokenHash(token)
	if err != nil {
		return page, err
	}
	if project == "" || len(project) > 128 || len(target) > 128 || len(after) > 128 {
		return page, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return page, err
	}
	defer tx.Rollback()
	if _, err := findHumanSession(ctx, tx, hash); err != nil {
		return page, err
	}
	var allowed bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM human_sessions u
 LEFT JOIN project_members m ON m.project=? AND m.user_id=u.user_id
 WHERE u.token_hash=? AND `+projectReadGrant+`)`, project, hash[:]).Scan(&allowed)
	if err != nil {
		return page, err
	}
	if !allowed {
		return page, ErrAccessDenied
	}
	var query string
	args := []any{project, after}
	switch kind {
	case "targets":
		query = `SELECT id,'','',CASE WHEN EXISTS(SELECT 1 FROM ci_container_targets ct WHERE ct.target=targets.id)
 THEN 'container' ELSE '' END,'' FROM targets WHERE project=? AND id>? ORDER BY id LIMIT 51`
	case "deployments":
		query = `SELECT id,target,release_id,state,digest FROM deployments WHERE project=? AND id>? ORDER BY id LIMIT 51`
	case "releases":
		var found bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM targets WHERE project=? AND id=?)`, project, target).Scan(&found); err != nil {
			return page, err
		}
		if !found {
			return page, sql.ErrNoRows
		}
		query = `SELECT id,'','','',digest FROM (
 SELECT id,digest FROM target_releases WHERE project=? AND target=?
 UNION ALL SELECT id,digest FROM releases g WHERE project=? AND NOT EXISTS(SELECT 1 FROM target_releases t WHERE t.project=g.project AND t.target=? AND t.id=g.id)
 ) WHERE id>? ORDER BY id LIMIT 51`
		args = []any{project, target, project, target, after}
	default:
		return page, ErrInvalid
	}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		var item CatalogItem
		if err := rows.Scan(&item.ID, &item.Target, &item.Release, &item.State, &item.Digest); err != nil {
			return page, err
		}
		if len(page.Items) == 50 {
			page.Next = page.Items[49].ID
			break
		}
		page.Items = append(page.Items, item)
	}
	if err := rows.Err(); err != nil {
		return page, err
	}
	if err := rows.Close(); err != nil {
		return page, err
	}
	return page, tx.Commit()
}

func (s *Store) CheckDatabase(ctx context.Context) error { return s.db.PingContext(ctx) }
