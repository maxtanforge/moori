package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/maxtanforge/moori/internal/core/release"
)

type ComposeMaintenanceSource struct {
	Project    string               `json:"project"`
	Target     string               `json:"target"`
	Deployment string               `json:"deployment"`
	Node       release.NodeIdentity `json:"node"`
}

type ComposeMaintenanceInput struct {
	Compose        string               `json:"compose"`
	ForceRecreate  bool                 `json:"force_recreate"`
	Node           release.NodeIdentity `json:"node"`
	SnapshotDigest string               `json:"snapshot_digest"`
}

type ComposeMaintenancePreview struct {
	Compose        string               `json:"compose"`
	SnapshotDigest string               `json:"snapshot_digest"`
	Target         string               `json:"target"`
	Node           release.NodeIdentity `json:"node"`
}

const lastComposeDeployment = `SELECT d.id FROM deployments d
 JOIN targets t ON t.project=d.project AND t.id=d.target
 WHERE d.target=? AND d.state='succeeded' AND d.release_id=t.last_success
 ORDER BY (SELECT MAX(id) FROM deployment_audit WHERE deployment_id=d.id AND state='succeeded') DESC, d.rowid DESC LIMIT 1`

func (s *Store) FindManagedCompose(ctx context.Context, token, agent, name string) (ComposeMaintenanceSource, error) {
	if _, err := platformActor(ctx, s.db, token); err != nil {
		return ComposeMaintenanceSource{}, err
	}
	source, err := findManagedComposeTarget(ctx, s.db, agent, name)
	if err != nil {
		return source, err
	}
	if err := s.db.QueryRowContext(ctx, lastComposeDeployment, source.Target).Scan(&source.Deployment); err != nil {
		return ComposeMaintenanceSource{}, err
	}
	if err := requireComposeBaseline(ctx, s.db, source.Target, source.Deployment, source.Node, false); err != nil {
		return ComposeMaintenanceSource{}, err
	}
	return source, nil
}

func findManagedComposeTarget(ctx context.Context, q queryRows, agent, name string) (ComposeMaintenanceSource, error) {
	if len(name) != len("moori-")+32 || !strings.HasPrefix(name, "moori-") {
		return ComposeMaintenanceSource{}, sql.ErrNoRows
	}
	rows, err := q.QueryContext(ctx, `SELECT t.project,t.id,a.generation FROM targets t
 JOIN target_assignments a ON a.target=t.id
 JOIN agent_certificates c ON c.agent=a.agent AND c.generation=a.generation
 WHERE a.agent=?`, agent)
	if err != nil {
		return ComposeMaintenanceSource{}, err
	}
	defer rows.Close()
	var source ComposeMaintenanceSource
	for rows.Next() {
		if err := rows.Scan(&source.Project, &source.Target, &source.Node.Generation); err != nil {
			return ComposeMaintenanceSource{}, err
		}
		if release.ComposeProjectName(source.Target) == name {
			break
		}
		source = ComposeMaintenanceSource{}
	}
	if err := rows.Close(); err != nil {
		return ComposeMaintenanceSource{}, err
	}
	if err := rows.Err(); err != nil {
		return ComposeMaintenanceSource{}, err
	}
	if source.Target == "" {
		return ComposeMaintenanceSource{}, sql.ErrNoRows
	}
	source.Node.Agent = agent
	return source, nil
}

func (s *Store) PreviewComposeMaintenance(ctx context.Context, token, project, source string, input ComposeMaintenanceInput) (ComposeMaintenancePreview, error) {
	a, artifact, err := s.composeMaintenanceArtifact(ctx, token, project, source, input)
	if err != nil {
		return ComposeMaintenancePreview{}, err
	}
	node, err := s.composeMaintenanceTarget(ctx, a.Project, a.Target)
	if err != nil {
		return ComposeMaintenancePreview{}, err
	}
	if input.Node.Valid() && input.Node != node {
		return ComposeMaintenancePreview{}, ErrConflict
	}
	if err := requireComposeBaseline(ctx, s.db, a.Target, source, node, true); err != nil {
		return ComposeMaintenancePreview{}, err
	}
	a.ExternalNode = &node
	snapshot, err := s.bindReleaseSnapshot(ctx, &a, artifact)
	if err != nil {
		return ComposeMaintenancePreview{}, err
	}
	if err := requireSecretVersions(ctx, s.db, project, a.Target, snapshot.SecretReferences()); err != nil {
		return ComposeMaintenancePreview{}, err
	}
	return ComposeMaintenancePreview{
		Compose: artifact.Compose(), SnapshotDigest: snapshot.Digest(), Target: a.Target, Node: node,
	}, nil
}

func (s *Store) SubmitComposeMaintenance(ctx context.Context, token, project, source, id, key string, input ComposeMaintenanceInput) (string, error) {
	if id == "" || key == "" || len(key) > 128 || !input.Node.Valid() || !validSnapshotDigest(input.SnapshotDigest) {
		return "", ErrInvalid
	}
	a, artifact, err := s.composeMaintenanceArtifact(ctx, token, project, source, input)
	if err != nil {
		return "", err
	}
	a.ID, a.Key, a.ExpectedSnapshotDigest = id, key, input.SnapshotDigest
	a.composeBase, a.composeNode = source, input.Node
	a.ExternalNode = &input.Node
	if existing, err := s.findHumanReleaseReplay(ctx, *a.session, a); !errors.Is(err, sql.ErrNoRows) {
		return existing, err
	}
	snapshot, err := s.bindReleaseSnapshot(ctx, &a, artifact)
	if err != nil {
		return "", err
	}
	a.consoleArtifact = &artifact
	return s.AdmitSnapshot(ctx, a, snapshot)
}

func (s *Store) composeMaintenanceArtifact(ctx context.Context, token, project, source string, input ComposeMaintenanceInput) (Admission, release.Release, error) {
	if _, err := platformActor(ctx, s.db, token); err != nil {
		return Admission{}, release.Release{}, err
	}
	session, err := newProjectSession(project, token)
	if err != nil {
		return Admission{}, release.Release{}, err
	}
	if source == "" || len(source) > 128 || input.ForceRecreate != (input.Compose == "") {
		return Admission{}, release.Release{}, ErrInvalid
	}
	a := Admission{Project: project, session: &session, forceRecreate: input.ForceRecreate}
	var document, digest string
	if err := s.db.QueryRowContext(ctx, `SELECT d.target,p.document,d.digest FROM deployments d
 JOIN snapshots p ON p.deployment_id=d.id WHERE d.project=? AND d.id=? AND d.state='succeeded'`, project, source).
		Scan(&a.Target, &document, &digest); err != nil {
		return Admission{}, release.Release{}, err
	}
	current, err := release.Verify(ctx, document, digest)
	if err != nil {
		return Admission{}, release.Release{}, err
	}
	compose := input.Compose
	if input.ForceRecreate {
		compose = current.Compose
	}
	artifact, err := release.Compile(ctx, []byte(compose), nil, nil, nil)
	if err != nil {
		return Admission{}, release.Release{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	a.Release = consoleTemplateReleasePrefix + "edit-" + strings.TrimPrefix(artifact.Digest(), "sha256:")
	return a, artifact, nil
}

func (s *Store) composeMaintenanceTarget(ctx context.Context, project, target string) (release.NodeIdentity, error) {
	var node release.NodeIdentity
	err := s.db.QueryRowContext(ctx, `SELECT a.agent,a.generation FROM targets t
 JOIN target_assignments a ON a.target=t.id
 JOIN agent_certificates c ON c.agent=a.agent AND c.generation=a.generation
 WHERE t.project=? AND t.id=?`, project, target).Scan(&node.Agent, &node.Generation)
	return node, err
}

func requireComposeBaseline(ctx context.Context, q queryRower, target, source string, node release.NodeIdentity, checkBusy bool) error {
	var current string
	err := q.QueryRowContext(ctx, lastComposeDeployment, target).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrConflict
	}
	if err != nil {
		return err
	}
	if current != source {
		return ErrConflict
	}
	var valid, busy bool
	err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM deployments d
 JOIN target_assignments a ON a.target=d.target
 JOIN agent_certificates c ON c.agent=a.agent AND c.generation=a.generation
 WHERE d.target=? AND d.id=? AND d.agent=a.agent AND a.agent=? AND a.generation=?),
 EXISTS(SELECT 1 FROM deployments WHERE target=? AND state NOT IN ('succeeded','failed','canceled','abandoned'))`,
		target, source, node.Agent, node.Generation, target).Scan(&valid, &busy)
	if err != nil {
		return err
	}
	if !valid || checkBusy && busy {
		return ErrConflict
	}
	return nil
}
