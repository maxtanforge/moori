package storage

import (
	"context"
	"errors"
	"fmt"
	"time"
)

var ErrAccessDenied = errors.New("project action is not granted")

const platformMembership = `EXISTS (SELECT 1 FROM platform_admins a WHERE a.user_id=u.user_id)`

const projectReadGrant = `(` + platformMembership + ` OR m.role IS NOT NULL)`
const projectAdminGrant = `(` + platformMembership + ` OR m.role='project_admin')`
const memberPageSize = 50

type ProjectMember struct {
	Username string `json:"username"`
	Role     string `json:"role"`
}

type ProjectMemberPage struct {
	Items []ProjectMember `json:"items"`
	Next  string          `json:"next,omitempty"`
}

type projectSession struct {
	project string
	hash    [32]byte
}

func newProjectSession(project, token string) (projectSession, error) {
	if project == "" || len(project) > 128 {
		return projectSession{}, ErrInvalid
	}
	hash, err := humanTokenHash(token)
	return projectSession{project: project, hash: hash}, err
}

func (p projectSession) authorize(ctx context.Context, q queryRower, allowOperator bool) (string, error) {
	actor, err := findHumanSession(ctx, q, p.hash)
	if err != nil {
		return "", err
	}
	var allowed bool
	err = q.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM platform_admins WHERE user_id=?) OR EXISTS (
 SELECT 1 FROM project_members WHERE project=? AND user_id=? AND (role='project_admin' OR (? AND role='operator')))`,
		actor, p.project, actor, allowOperator).Scan(&allowed)
	if err != nil {
		return "", err
	}
	if !allowed {
		return "", ErrAccessDenied
	}
	return actor, nil
}

// SetPlatformAdmin is restricted to the trusted local CLI. It also provides
// account recovery if all browser administrators lose access.
func (s *Store) SetPlatformAdmin(ctx context.Context, username string, granted bool) error {
	username = normalizeUsername(username)
	if !validUsername(username) {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var subject string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM human_users WHERE username=?`, username).Scan(&subject); err != nil {
		return err
	}
	statement := `DELETE FROM platform_admins WHERE user_id=?`
	if granted {
		statement = `INSERT INTO platform_admins VALUES (?) ON CONFLICT DO NOTHING`
	}
	res, err := tx.ExecContext(ctx, statement, subject)
	if err != nil {
		return err
	}
	changed, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if changed > 0 {
		if err := addProjectAudit(ctx, tx, "", "platform_admin.changed", "local", subject, fmt.Sprintf("granted=%t", granted)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) SetProjectMember(ctx context.Context, token, project, username, role string) error {
	p, err := newProjectSession(project, token)
	if err != nil {
		return err
	}
	username = normalizeUsername(username)
	if !validUsername(username) || role != "none" && role != "viewer" && role != "operator" && role != "project_admin" {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	actor, err := p.authorize(ctx, tx, false)
	if err != nil {
		return err
	}
	var subject, oldRole string
	err = tx.QueryRowContext(ctx, `SELECT u.id,coalesce(m.role,'none') FROM human_users u
 LEFT JOIN project_members m ON m.project=? AND m.user_id=u.id
 WHERE u.username=? AND EXISTS (SELECT 1 FROM targets WHERE project=?)`, project, username, project).Scan(&subject, &oldRole)
	if err != nil {
		return err
	}
	if oldRole == role {
		return tx.Commit()
	}
	if role == "none" {
		_, err = tx.ExecContext(ctx, `DELETE FROM project_members WHERE project=? AND user_id=?`, project, subject)
	} else {
		_, err = tx.ExecContext(ctx, `INSERT INTO project_members(project,user_id,role) VALUES (?,?,?)
 ON CONFLICT(project,user_id) DO UPDATE SET role=excluded.role`, project, subject, role)
	}
	if err != nil {
		return err
	}
	detail := fmt.Sprintf("role=%s (previous role=%s)", role, oldRole)
	if err := addProjectAudit(ctx, tx, project, "member.changed", actor, subject, detail); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ListProjectMembers(ctx context.Context, token, project, after string) (ProjectMemberPage, error) {
	p, err := newProjectSession(project, token)
	if err != nil {
		return ProjectMemberPage{}, err
	}
	if after != "" && !validUsername(after) {
		return ProjectMemberPage{}, ErrInvalid
	}
	rows, err := s.db.QueryContext(ctx, `WITH members AS (
 SELECT h.username,pm.role FROM project_members pm JOIN human_users h ON h.id=pm.user_id
 WHERE pm.project=? AND h.username>? ORDER BY h.username LIMIT ?
 ) SELECT coalesce(n.username,''),coalesce(n.role,'') FROM human_sessions u
 LEFT JOIN project_members m ON m.project=? AND m.user_id=u.user_id
 LEFT JOIN members n ON TRUE
 WHERE u.token_hash=? AND u.expires_ms>? AND `+projectAdminGrant+` ORDER BY n.username`,
		project, after, memberPageSize+1, project, p.hash[:], time.Now().UnixMilli())
	if err != nil {
		return ProjectMemberPage{}, err
	}
	defer rows.Close()
	page := ProjectMemberPage{Items: make([]ProjectMember, 0, memberPageSize)}
	for rows.Next() {
		var item ProjectMember
		if err := rows.Scan(&item.Username, &item.Role); err != nil {
			return ProjectMemberPage{}, err
		}
		if item.Username == "" {
			return page, nil
		}
		if len(page.Items) == memberPageSize {
			page.Next = page.Items[memberPageSize-1].Username
			break
		}
		page.Items = append(page.Items, item)
	}
	if err := rows.Err(); err != nil {
		return ProjectMemberPage{}, err
	}
	if len(page.Items) == 0 {
		return ProjectMemberPage{}, ErrAccessDenied
	}
	return page, nil
}
