package storage

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/maxtanforge/moori/internal/core/release"
)

const consoleTemplateReleasePrefix = "console-template-"

type ConsoleTemplateSource struct {
	Project    string `json:"project"`
	Deployment string `json:"deployment"`
}

type ConsoleTemplateInput struct {
	Name        string                     `json:"name"`
	Description string                     `json:"description"`
	Compose     string                     `json:"compose"`
	Source      *ConsoleTemplateSource     `json:"source"`
	Parameters  release.TemplateParameters `json:"parameters"`
}

type ConsoleTemplate struct {
	ID          string                   `json:"id"`
	Name        string                   `json:"name"`
	Description string                   `json:"description"`
	Versions    []ConsoleTemplateVersion `json:"versions"`
}

type ConsoleTemplateVersion struct {
	Version        int64                      `json:"version"`
	Digest         string                     `json:"digest"`
	CreatedAt      time.Time                  `json:"createdAt"`
	Parameters     release.TemplateParameters `json:"parameters"`
	SecretServices map[string][]string        `json:"secretServices,omitempty"`
}

func (s *Store) SaveConsoleTemplate(ctx context.Context, token, id string, in ConsoleTemplateInput) (ConsoleTemplate, error) {
	if _, err := platformActor(ctx, s.db, token); err != nil {
		return ConsoleTemplate{}, err
	}
	create := id == ""
	in.Name = strings.TrimSpace(in.Name)
	in.Description = strings.TrimSpace(in.Description)
	if (in.Compose == "") == (in.Source == nil) || len(id) > 64 ||
		create && (in.Name == "" || len(in.Name) > 128 || len(in.Description) > 2048) ||
		!create && (in.Name != "" || in.Description != "") || strings.ContainsAny(in.Name, "\x00\r\n") {
		return ConsoleTemplate{}, ErrInvalid
	}
	var source release.Verified
	if in.Source != nil {
		verified, err := s.composeTemplateSource(ctx, *in.Source)
		if err != nil {
			return ConsoleTemplate{}, err
		}
		source = verified
		in.Compose = verified.Compose
	}
	artifact, fields, err := release.PrepareTemplate(ctx, []byte(in.Compose), in.Parameters)
	if err != nil {
		return ConsoleTemplate{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	secretServices, err := artifact.TemplateSecretFiles()
	if err != nil {
		return ConsoleTemplate{}, err
	}
	mergeTemplateSecrets(secretServices, source.SecretServices)
	mounts, err := json.Marshal(secretServices)
	if err != nil {
		return ConsoleTemplate{}, err
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		return ConsoleTemplate{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ConsoleTemplate{}, err
	}
	defer tx.Rollback()
	actor, err := platformActor(ctx, tx, token)
	if err != nil {
		return ConsoleTemplate{}, err
	}
	if create {
		id = "tpl-" + rand.Text()
		res, err := tx.ExecContext(ctx, `INSERT INTO console_templates(id,name,description) VALUES (?,?,?)
 ON CONFLICT(name) DO NOTHING`, id, in.Name, in.Description)
		if err := expectOne(res, err, ErrConflict); err != nil {
			return ConsoleTemplate{}, err
		}
	}
	err = tx.QueryRowContext(ctx, "SELECT id FROM console_templates WHERE id=?", id).Scan(&id)
	if err != nil {
		return ConsoleTemplate{}, err
	}
	var version int64
	if err := tx.QueryRowContext(ctx, "SELECT coalesce(max(version), 0)+1 FROM console_template_versions WHERE template_id=?", id).Scan(&version); err != nil {
		return ConsoleTemplate{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO console_template_versions(template_id,version,compose,digest,parameters,secret_services,created_ms)
 VALUES (?,?,?,?,?,?,?)`, id, version, artifact.Compose(), artifact.Digest(), string(raw), string(mounts), time.Now().UnixMilli()); err != nil {
		return ConsoleTemplate{}, err
	}
	if err := addProjectAudit(ctx, tx, "", "compose_template.version_created", actor, id, fmt.Sprintf("version=%d", version)); err != nil {
		return ConsoleTemplate{}, err
	}
	list, err := readConsoleTemplates(ctx, tx, id)
	if err != nil {
		return ConsoleTemplate{}, err
	}
	if err := tx.Commit(); err != nil {
		return ConsoleTemplate{}, err
	}
	return list[0], nil
}

func (s *Store) composeTemplateSource(ctx context.Context, src ConsoleTemplateSource) (release.Verified, error) {
	if src.Project == "" || src.Deployment == "" || len(src.Project) > 128 || len(src.Deployment) > 128 {
		return release.Verified{}, ErrInvalid
	}
	var snapshot, digest string
	err := s.db.QueryRowContext(ctx, `SELECT p.document,d.digest FROM deployments d
 JOIN snapshots p ON p.deployment_id=d.id WHERE d.project=? AND d.id=?`, src.Project, src.Deployment).Scan(&snapshot, &digest)
	if err != nil {
		return release.Verified{}, err
	}
	return release.Verify(ctx, snapshot, digest)
}

func (s *Store) ListConsoleTemplates(ctx context.Context, token string) ([]ConsoleTemplate, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := platformActor(ctx, tx, token); err != nil {
		return nil, err
	}
	items, err := readConsoleTemplates(ctx, tx, "")
	if err != nil {
		return nil, err
	}
	return items, tx.Commit()
}

type queryRows interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func readConsoleTemplates(ctx context.Context, q queryRows, id string) ([]ConsoleTemplate, error) {
	query := `SELECT t.id,t.name,t.description,v.version,v.digest,v.created_ms,v.parameters,v.secret_services FROM console_templates t
 JOIN console_template_versions v ON v.template_id=t.id`
	var args []any
	if id != "" {
		query += " WHERE t.id=?"
		args = append(args, id)
	}
	rows, err := q.QueryContext(ctx, query+" ORDER BY t.name COLLATE NOCASE,t.id,v.version DESC", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []ConsoleTemplate{}
	for rows.Next() {
		var t ConsoleTemplate
		var v ConsoleTemplateVersion
		var created int64
		var raw, mounts string
		if err := rows.Scan(&t.ID, &t.Name, &t.Description, &v.Version, &v.Digest, &created, &raw, &mounts); err != nil {
			return nil, err
		}
		v.CreatedAt = time.UnixMilli(created).UTC()
		if err := json.Unmarshal([]byte(raw), &v.Parameters); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(mounts), &v.SecretServices); err != nil {
			return nil, err
		}
		if len(items) == 0 || items[len(items)-1].ID != t.ID {
			items = append(items, t)
		}
		item := &items[len(items)-1]
		item.Versions = append(item.Versions, v)
	}
	return items, rows.Err()
}

func (s *Store) AdmitConsoleTemplate(ctx context.Context, token, id string, version int64, a Admission, overrides release.TemplateOverrides) (string, error) {
	if _, err := platformActor(ctx, s.db, token); err != nil {
		return "", err
	}
	session, err := newProjectSession(a.Project, token)
	if err != nil {
		return "", err
	}
	if version <= 0 || id == "" || len(id) > 64 || a.Target == "" || len(a.Target) > 128 ||
		a.Key == "" || len(a.Key) > 128 || a.ID == "" {
		return "", ErrInvalid
	}
	compiled, requirements, err := s.renderConsoleArtifact(ctx, id, version, overrides)
	if err != nil {
		return "", err
	}
	if err := validateTemplateNode(compiled, a.ExternalNode); err != nil {
		return "", err
	}
	if compiled.UsesExternalResources() && !validSnapshotDigest(a.ExpectedSnapshotDigest) {
		return "", fmt.Errorf("%w: deployment requires a preview snapshot digest", ErrInvalid)
	}
	a.Release = fmt.Sprintf("%s%s-v%d-%s", consoleTemplateReleasePrefix, id, version, strings.TrimPrefix(compiled.Digest(), "sha256:"))
	if existing, err := s.findHumanReleaseReplay(ctx, session, a); !errors.Is(err, sql.ErrNoRows) {
		return existing, err
	}
	if err := s.requireConsoleTarget(ctx, a.Project, a.Target); err != nil {
		return "", err
	}
	a.templateSecretServices = requirements
	snapshot, err := s.bindReleaseSnapshot(ctx, &a, compiled)
	if err != nil {
		return "", err
	}
	a.session = &session
	a.consoleArtifact = &compiled
	return s.AdmitSnapshot(ctx, a, snapshot)
}

type ConsoleTemplatePreview struct {
	Compose        string `json:"compose"`
	Digest         string `json:"digest"`
	SnapshotDigest string `json:"snapshot_digest"`
}

func (s *Store) PreviewConsoleTemplate(ctx context.Context, token, id string, version int64, project, target string, node *release.NodeIdentity, overrides release.TemplateOverrides) (ConsoleTemplatePreview, error) {
	if _, err := platformActor(ctx, s.db, token); err != nil {
		return ConsoleTemplatePreview{}, err
	}
	if version <= 0 || id == "" || len(id) > 64 || project == "" || len(project) > 128 || target == "" || len(target) > 128 {
		return ConsoleTemplatePreview{}, ErrInvalid
	}
	if err := s.requireConsoleTarget(ctx, project, target); err != nil {
		return ConsoleTemplatePreview{}, err
	}
	compiled, requirements, err := s.renderConsoleArtifact(ctx, id, version, overrides)
	if err != nil {
		return ConsoleTemplatePreview{}, err
	}
	if err := validateTemplateNode(compiled, node); err != nil {
		return ConsoleTemplatePreview{}, err
	}
	admission := Admission{Project: project, Target: target, ExternalNode: node, templateSecretServices: requirements}
	snapshot, err := s.bindReleaseSnapshot(ctx, &admission, compiled)
	if err != nil {
		return ConsoleTemplatePreview{}, err
	}
	if err := requireExternalSnapshotNode(ctx, s.db, project, snapshot); err != nil {
		return ConsoleTemplatePreview{}, err
	}
	if err := requireSecretVersions(ctx, s.db, project, target, snapshot.SecretReferences()); err != nil {
		return ConsoleTemplatePreview{}, err
	}
	return ConsoleTemplatePreview{Compose: compiled.Compose(), Digest: compiled.Digest(), SnapshotDigest: snapshot.Digest()}, nil
}

func validateTemplateNode(compiled release.Release, node *release.NodeIdentity) error {
	if compiled.UsesExternalResources() {
		if node == nil || !node.Valid() {
			return fmt.Errorf("%w: external resources require the target Agent and generation", ErrInvalid)
		}
	} else if node != nil {
		return fmt.Errorf("%w: a node may only be specified for external resources", ErrInvalid)
	}
	return nil
}

func validSnapshotDigest(digest string) bool {
	prefix, ok := strings.CutPrefix(digest, "sha256:")
	return ok && len(prefix) == 64 && strings.IndexFunc(prefix, func(r rune) bool {
		return (r < '0' || r > '9') && (r < 'a' || r > 'f')
	}) == -1
}

func requireExternalSnapshotNode(ctx context.Context, q queryRower, project string, snapshot release.Snapshot) error {
	node := snapshot.ExternalNode()
	if !snapshot.UsesExternalResources() && !node.Valid() {
		return nil
	}
	if !node.Valid() {
		return fmt.Errorf("%w: external resources have no recorded node assignment", ErrConflict)
	}
	var matches bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM targets t
 JOIN target_assignments a ON a.target=t.id
 JOIN agent_certificates c ON c.agent=a.agent AND c.generation=a.generation
 WHERE t.project=? AND t.id=? AND a.agent=? AND a.generation=?)`,
		project, snapshot.Target(), node.Agent, node.Generation).Scan(&matches); err != nil {
		return err
	}
	if !matches {
		return fmt.Errorf("%w: target Agent assignment changed", ErrConflict)
	}
	return nil
}

func (s *Store) renderConsoleArtifact(ctx context.Context, id string, version int64, overrides release.TemplateOverrides) (release.Release, map[string][]string, error) {
	var document, raw, mounts string
	err := s.db.QueryRowContext(ctx, `SELECT compose,parameters,secret_services FROM console_template_versions WHERE template_id=? AND version=?`, id, version).
		Scan(&document, &raw, &mounts)
	if err != nil {
		return release.Release{}, nil, err
	}
	var fields release.TemplateParameters
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return release.Release{}, nil, err
	}
	compiled, err := release.InstantiateTemplate(ctx, document, fields, overrides)
	if err != nil {
		return release.Release{}, nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	var stored map[string][]string
	if err := json.Unmarshal([]byte(mounts), &stored); err != nil {
		return release.Release{}, nil, err
	}
	requirements, err := compiled.TemplateSecretFiles()
	if err != nil {
		return release.Release{}, nil, err
	}
	mergeTemplateSecrets(requirements, stored)
	return compiled, requirements, nil
}

func (s *Store) requireConsoleTarget(ctx context.Context, project, target string) error {
	var found string
	return s.db.QueryRowContext(ctx, "SELECT id FROM targets WHERE project=? AND id=?", project, target).Scan(&found)
}

func mergeTemplateSecrets(dst, extra map[string][]string) {
	for service, names := range extra {
		for _, name := range names {
			if !slices.Contains(dst[service], name) {
				dst[service] = append(dst[service], name)
			}
		}
		slices.Sort(dst[service])
	}
}

func requireTemplateSecrets(bindings SecretBindings, required map[string][]string) error {
	for service, names := range required {
		for _, name := range names {
			if !slices.Contains(bindings.Services[service], name) {
				return fmt.Errorf("%w: target must grant secret %q to service %q", ErrSecretUnavailable, name, service)
			}
		}
	}
	return nil
}
func authorizeHumanRelease(ctx context.Context, q queryRower, session projectSession, id string) (string, error) {
	actor, err := session.authorize(ctx, q, true)
	if err != nil || !strings.HasPrefix(id, consoleTemplateReleasePrefix) {
		return actor, err
	}
	var granted bool
	if err := q.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM platform_admins WHERE user_id=?)", actor).Scan(&granted); err != nil {
		return "", err
	}
	if !granted {
		return "", ErrAccessDenied
	}
	return actor, nil
}

func requireUnrestrictedArtifact(artifact release.Release) error {
	volumes, networks, err := release.ExternalResources(artifact.Compose())
	if err != nil {
		return err
	}
	if len(volumes) > 0 || len(networks) > 0 {
		return fmt.Errorf("%w: external resources require deployment through the Compose template library", ErrInvalid)
	}
	return nil
}
