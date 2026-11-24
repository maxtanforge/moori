package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/maxtanforge/moori/internal/core/release"
)

type Publication struct {
	Project  string            `json:"project"`
	Target   string            `json:"target"`
	ID       string            `json:"id"`
	Compose  string            `json:"compose,omitempty"`
	Template string            `json:"template,omitempty"`
	Images   map[string]string `json:"images,omitempty"`
}

type PublishedRelease struct {
	ID     string `json:"id"`
	Digest string `json:"digest"`
}

func requirePublish(ctx context.Context, q queryRower, hash [32]byte, project, target string) error {
	var permitted bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM service_credentials c JOIN ci_credential_scopes g ON g.credential=c.id
 WHERE c.token_hash=? AND g.project=? AND g.target=? AND c.revoked=0 AND c.expires_ms>? AND g.can_publish=1
 AND NOT EXISTS(SELECT 1 FROM ci_container_targets ct WHERE ct.target=g.target))`, hash[:], project, target, time.Now().UnixMilli()).Scan(&permitted)
	if err != nil {
		return err
	}
	if !permitted {
		return ErrForbidden
	}
	return nil
}

func (s *Store) PublishForService(ctx context.Context, token string, p Publication) (PublishedRelease, error) {
	hash, err := credentialHash(token)
	if err != nil {
		return PublishedRelease{}, err
	}
	if p.Project == "" || p.Target == "" || p.ID == "" || strings.HasPrefix(p.ID, consoleTemplateReleasePrefix) || (p.Compose == "") == (p.Template == "") {
		return PublishedRelease{}, ErrInvalid
	}
	if err := requirePublish(ctx, s.db, hash, p.Project, p.Target); err != nil {
		return PublishedRelease{}, err
	}
	compiled, err := s.compilePublication(ctx, p)
	if err != nil {
		return PublishedRelease{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PublishedRelease{}, err
	}
	defer tx.Rollback()
	if err := requirePublish(ctx, tx, hash, p.Project, p.Target); err != nil {
		return PublishedRelease{}, err
	}
	if err := insertTargetRelease(ctx, tx, p.Project, p.Target, p.ID, compiled); err != nil {
		return PublishedRelease{}, err
	}
	if err := tx.Commit(); err != nil {
		return PublishedRelease{}, err
	}
	return PublishedRelease{ID: p.ID, Digest: compiled.Digest()}, nil
}

func insertTargetRelease(ctx context.Context, tx *sql.Tx, project, target, id string, compiled release.Release) error {
	res, err := tx.ExecContext(ctx, `INSERT INTO target_releases(project,target,id,digest,compose) SELECT ?,?,?,?,?
 WHERE NOT EXISTS (SELECT 1 FROM releases WHERE project=? AND id=?)
 ON CONFLICT(project,target,id) DO UPDATE SET digest=excluded.digest WHERE target_releases.digest=excluded.digest`,
		project, target, id, compiled.Digest(), compiled.Compose(), project, id)
	return expectOne(res, err, ErrConflict)
}

func (s *Store) compilePublication(ctx context.Context, p Publication) (release.Release, error) {
	address := p.Compose
	var allowed []string
	if p.Template == "" {
		for service := range p.Images {
			allowed = append(allowed, service)
		}
		slices.Sort(allowed)
	}
	if p.Template != "" {
		var raw string
		if err := s.db.QueryRowContext(ctx, `SELECT compose,allowed_images FROM target_templates WHERE project=? AND target=? AND id=?`,
			p.Project, p.Target, p.Template).Scan(&address, &raw); err != nil {
			return release.Release{}, err
		}
		if err := json.Unmarshal([]byte(raw), &allowed); err != nil {
			return release.Release{}, err
		}
		if len(allowed) != len(p.Images) {
			return release.Release{}, ErrInvalid
		}
	}
	compiled, err := release.Compile(ctx, []byte(address), p.Images, allowed, nil)
	if err != nil {
		return release.Release{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if err := requireUnrestrictedArtifact(compiled); err != nil {
		return release.Release{}, err
	}
	return compiled, nil
}

func (s *Store) PublishTemplate(ctx context.Context, project, target, id, compose string, allowed []string) error {
	if project == "" || target == "" || id == "" || len(allowed) == 0 || len(allowed) > 64 {
		return ErrInvalid
	}
	placeholders := make(map[string]string, len(allowed))
	for _, key := range allowed {
		if key == "" || placeholders[key] != "" {
			return ErrInvalid
		}
		placeholders[key] = "docker.io/library/busybox@sha256:" + strings.Repeat("1", 64)
	}
	if _, err := release.Compile(ctx, []byte(compose), placeholders, allowed, nil); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	sorted := slices.Clone(allowed)
	slices.Sort(sorted)
	raw, err := json.Marshal(sorted)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO target_templates(project,target,id,compose,allowed_images) VALUES (?,?,?,?,?)
 ON CONFLICT(project,target,id) DO UPDATE SET id=excluded.id
 WHERE target_templates.compose=excluded.compose AND target_templates.allowed_images=excluded.allowed_images`,
		project, target, id, compose, string(raw))
	return expectOne(res, err, ErrConflict)
}

func (s *Store) ReadPublishedRelease(ctx context.Context, token, project, target, id string) (PublishedRelease, error) {
	hash, err := credentialHash(token)
	if err != nil {
		return PublishedRelease{}, err
	}
	var item PublishedRelease
	item.ID = id
	err = s.db.QueryRowContext(ctx, `SELECT v.digest FROM (`+visibleReleases+`) v WHERE EXISTS(
 SELECT 1 FROM service_credentials c JOIN ci_credential_scopes g ON g.credential=c.id
 WHERE c.token_hash=? AND g.project=? AND g.target=? AND c.revoked=0 AND c.expires_ms>?)
 ORDER BY v.precedence LIMIT 1`,
		project, target, id, project, id, hash[:], project, target, time.Now().UnixMilli()).Scan(&item.Digest)
	return item, err
}
