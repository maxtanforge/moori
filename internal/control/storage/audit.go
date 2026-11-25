package storage

import (
	"context"
	"database/sql"
	"math"
	"time"
)

const auditPageSize = 50

type ProjectAuditEvent struct {
	ID          int64  `json:"id"`
	Action      string `json:"action"`
	Actor       string `json:"actor"`
	ActorName   string `json:"actor_name,omitempty"`
	Subject     string `json:"subject"`
	SubjectName string `json:"subject_name,omitempty"`
	Detail      string `json:"detail"`
	CreatedAt   string `json:"created_at"`
}

type ProjectAuditPage struct {
	Items []ProjectAuditEvent `json:"items"`
	Next  int64               `json:"next,omitempty"`
}

func addProjectAudit(ctx context.Context, tx *sql.Tx, project, action, actor, subject, detail string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO project_audit(project,action,actor,subject,detail) VALUES (?,?,?,?,?)`,
		project, action, actor, subject, detail)
	return err
}

// AddPlatformAudit records a node-scoped administrative event outside any
// transaction. Empty-project events surface in the platform audit view.
func (s *Store) AddPlatformAudit(ctx context.Context, action, actor, subject, detail string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO project_audit(project,action,actor,subject,detail) VALUES ('',?,?,?,?)`,
		action, actor, subject, detail)
	return err
}

// ReadProjectAudit accepts an empty project for platform administration events.
// The left join distinguishes a grant with no events from a missing grant.
func (s *Store) ReadProjectAudit(ctx context.Context, token, project string, after int64) (ProjectAuditPage, error) {
	return s.readProjectAudit(ctx, token, project, after, false)
}

// ReadProjectAuditBefore returns audit events newest first. A zero cursor starts
// at the newest event, and a positive cursor excludes that event and newer events.
func (s *Store) ReadProjectAuditBefore(ctx context.Context, token, project string, before int64) (ProjectAuditPage, error) {
	return s.readProjectAudit(ctx, token, project, before, true)
}

func (s *Store) readProjectAudit(ctx context.Context, token, project string, cursor int64, newestFirst bool) (ProjectAuditPage, error) {
	if cursor < 0 || len(project) > 128 {
		return ProjectAuditPage{}, ErrInvalid
	}
	hash, err := humanTokenHash(token)
	if err != nil {
		return ProjectAuditPage{}, err
	}
	eventMatch, order := "e.id>?", "e.id"
	names, nameJoin := "'',''", ""
	if newestFirst {
		eventMatch, order = "e.id<=?", "e.id DESC"
		if cursor == 0 {
			cursor = math.MaxInt64
		} else {
			cursor--
		}
		names = "coalesce(actor.username,''),coalesce(subject.username,'')"
		nameJoin = `
 LEFT JOIN human_users actor ON actor.id=e.actor
 LEFT JOIN human_users subject ON subject.id=e.subject
 AND (e.action IN ('platform.initialized','platform_admin.changed','member.changed')
 OR e.action LIKE 'account.%')`
	}
	rows, err := s.db.QueryContext(ctx, `SELECT e.id,coalesce(e.action,''),coalesce(e.actor,''),
 coalesce(e.subject,''),coalesce(e.detail,''),coalesce(e.created_at,''),`+names+`
 FROM human_sessions u
 LEFT JOIN project_members m ON m.project=? AND m.user_id=u.user_id
 LEFT JOIN project_audit e ON e.project=? AND `+eventMatch+nameJoin+`
 WHERE u.token_hash=? AND u.expires_ms>? AND `+projectAdminGrant+`
 ORDER BY `+order+` LIMIT ?`, project, project, cursor, hash[:], time.Now().UnixMilli(), auditPageSize+1)
	if err != nil {
		return ProjectAuditPage{}, err
	}
	defer rows.Close()
	page := ProjectAuditPage{Items: make([]ProjectAuditEvent, 0, auditPageSize)}
	for rows.Next() {
		var id sql.NullInt64
		var item ProjectAuditEvent
		if err := rows.Scan(&id, &item.Action, &item.Actor, &item.Subject, &item.Detail, &item.CreatedAt, &item.ActorName, &item.SubjectName); err != nil {
			return ProjectAuditPage{}, err
		}
		if !id.Valid {
			return page, nil
		}
		if len(page.Items) == auditPageSize {
			page.Next = page.Items[auditPageSize-1].ID
			break
		}
		item.ID = id.Int64
		page.Items = append(page.Items, item)
	}
	if err := rows.Err(); err != nil {
		return ProjectAuditPage{}, err
	}
	if len(page.Items) == 0 {
		return ProjectAuditPage{}, ErrAccessDenied
	}
	return page, nil
}
