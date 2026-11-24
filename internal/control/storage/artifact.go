package storage

import (
	"context"
	"strings"

	"github.com/maxtanforge/moori/internal/core/release"
)

const visibleReleases = `SELECT compose,digest,0 AS precedence FROM target_releases WHERE project=? AND target=? AND id=?
 UNION ALL SELECT compose,digest,1 AS precedence FROM releases WHERE project=? AND id=?`

// PublishRelease saves a compiled artifact. Reusing an ID with different
// content is a conflict, including after a restart.
func (s *Store) PublishRelease(ctx context.Context, project, id string, r release.Release) error {
	if project == "" || id == "" || strings.HasPrefix(id, consoleTemplateReleasePrefix) || r.Digest() == "" {
		return ErrInvalid
	}
	if err := requireUnrestrictedArtifact(r); err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO releases(project,id,digest,compose) VALUES (?,?,?,?)
 ON CONFLICT(project,id) DO UPDATE SET digest=excluded.digest WHERE releases.digest=excluded.digest`,
		project, id, r.Digest(), r.Compose())
	return expectOne(res, err, ErrConflict)
}

func (s *Store) ExecutionSnapshot(ctx context.Context, id string) (string, error) {
	var document string
	err := s.db.QueryRowContext(ctx, "SELECT document FROM snapshots WHERE deployment_id=?", id).Scan(&document)
	return document, err
}
