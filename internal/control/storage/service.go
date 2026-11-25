package storage

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"strings"
	"time"

	"github.com/maxtanforge/moori/internal/core/deployment"
	"github.com/maxtanforge/moori/internal/core/release"
)

const servicePrefix = "moori_ci_"

func (s *Store) IssueServiceCredential(ctx context.Context, id, project, target string, expires time.Time) (string, error) {
	return s.issueServiceCredential(ctx, id, project, target, expires, false)
}

func (s *Store) IssuePublishingCredential(ctx context.Context, id, project, target string, expires time.Time) (string, error) {
	return s.issueServiceCredential(ctx, id, project, target, expires, true)
}

func (s *Store) issueServiceCredential(ctx context.Context, id, project, target string, expires time.Time, publish bool) (string, error) {
	return issueServiceCredential(ctx, s.db, id, project, target, expires, publish)
}

type serviceCredentialWriter interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func issueServiceCredential(ctx context.Context, writer serviceCredentialWriter, id, project, target string, expires time.Time, publish bool) (string, error) {
	if id == "" || project == "" || target == "" || expires.UnixMilli() <= time.Now().UnixMilli() {
		return "", ErrInvalid
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return "", err
	}
	token := servicePrefix + base64.RawURLEncoding.EncodeToString(secret)
	hash := sha256.Sum256([]byte(token))
	res, err := writer.ExecContext(ctx, `INSERT INTO service_credentials(id,project,target,token_hash,expires_ms,can_publish)
 VALUES (?,?,?,?,?,?) ON CONFLICT DO NOTHING`, id, project, target, hash[:], expires.UnixMilli(), publish)
	if err = expectOne(res, err, ErrConflict); err != nil {
		return "", err
	}
	return token, nil
}

type ServiceDeployment struct {
	ID         string           `json:"id"`
	State      deployment.State `json:"state"`
	Target     string           `json:"target"`
	Release    string           `json:"release"`
	RollbackOf string           `json:"rollback_of,omitempty"`
	Execution  string           `json:"-"`
}

// ReadServiceDeployment returns only a deployment submitted by the bearer.
func (s *Store) ReadServiceDeployment(ctx context.Context, token, id string) (ServiceDeployment, error) {
	hash, err := credentialHash(token)
	if err != nil {
		return ServiceDeployment{}, err
	}
	var item ServiceDeployment
	err = s.db.QueryRowContext(ctx, `SELECT d.id,d.state,d.target,d.release_id,coalesce(d.rollback_of,''),coalesce(d.execution_id,'') FROM deployments d
 JOIN service_credentials c ON c.id=d.credential_id
 WHERE d.id=? AND c.token_hash=? AND c.revoked=0 AND c.expires_ms>?`,
		id, hash[:], time.Now().UnixMilli()).Scan(&item.ID, &item.State, &item.Target, &item.Release, &item.RollbackOf, &item.Execution)
	return item, err
}

func (s *Store) RevokeServiceCredential(ctx context.Context, id string) error {
	return s.revokeServiceCredential(ctx, "", "", id)
}

func (s *Store) revokeServiceCredential(ctx context.Context, session, scopedTarget, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	actor := "local"
	if scopedTarget != "" {
		actor, err = platformActor(ctx, tx, session)
		if err != nil {
			return err
		}
	}
	var revoked bool
	if err := tx.QueryRowContext(ctx, `SELECT revoked FROM service_credentials c WHERE id=? AND (?='' OR EXISTS(
 SELECT 1 FROM ci_credential_scopes g WHERE g.credential=c.id AND g.target=?))`, id, scopedTarget, scopedTarget).Scan(&revoked); err != nil {
		return err
	}
	if revoked {
		return tx.Commit()
	}
	if _, err := tx.ExecContext(ctx, `UPDATE service_credentials SET revoked=1 WHERE id=?`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE node_operations SET state='failed',error='The CI credential was revoked before dispatch.'
 WHERE state='queued' AND id IN (SELECT operation FROM ci_container_deliveries WHERE credential=?)`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO project_audit(project,action,actor,subject,detail)
 SELECT project,'ci_credential.revoked',?,?,target FROM ci_credential_scopes WHERE credential=?`, actor, id, id); err != nil {
		return err
	}
	return tx.Commit()
}

func credentialHash(token string) ([32]byte, error) {
	if len(token) != len(servicePrefix)+base64.RawURLEncoding.EncodedLen(32) || !strings.HasPrefix(token, servicePrefix) {
		return [32]byte{}, ErrForbidden
	}
	return sha256.Sum256([]byte(token)), nil
}

type queryRower interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func findCredential(ctx context.Context, q queryRower, hash [32]byte, project, target string) (string, error) {
	var id string
	err := q.QueryRowContext(ctx, `SELECT c.id FROM service_credentials c JOIN ci_credential_scopes g ON g.credential=c.id
 WHERE c.token_hash=? AND g.project=? AND g.target=? AND c.revoked=0 AND c.expires_ms>?`, hash[:], project, target, time.Now().UnixMilli()).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrForbidden
	}
	return id, err
}

func (s *Store) AdmitServiceSnapshot(ctx context.Context, token string, a Admission, snapshot release.Snapshot) (string, error) {
	hash, err := credentialHash(token)
	if err != nil {
		return "", err
	}
	return s.admitSnapshot(ctx, a, snapshot, &hash, "")
}

// AdmitPublishedRelease builds a CI deployment from a pre-published artifact.
// CI supplies identifiers only. Authorization is checked before lookup and
// checked again inside admission so a concurrent revocation wins.
func (s *Store) AdmitPublishedRelease(ctx context.Context, token string, a Admission) (string, error) {
	return s.admitPublishedRelease(ctx, token, a, "")
}

// AdmitIntegrationRelease preserves a verified integration's idempotency
// scope when its target-scoped credential is rotated.
func (s *Store) AdmitIntegrationRelease(ctx context.Context, token, integration string, a Admission) (string, error) {
	if integration == "" {
		return "", ErrInvalid
	}
	return s.admitPublishedRelease(ctx, token, a, "integration:"+integration)
}

func (s *Store) admitPublishedRelease(ctx context.Context, token string, a Admission, principal string) (string, error) {
	hash, err := credentialHash(token)
	if err != nil {
		return "", err
	}
	if _, err := findCredential(ctx, s.db, hash, a.Project, a.Target); err != nil {
		return "", err
	}
	snapshot, err := s.snapshotPublishedRelease(ctx, &a)
	if err != nil {
		return "", err
	}
	return s.admitSnapshot(ctx, a, snapshot, &hash, principal)
}

func (s *Store) AdmitHumanRelease(ctx context.Context, token string, a Admission) (string, error) {
	if strings.HasPrefix(a.Release, consoleTemplateReleasePrefix) {
		return "", ErrAccessDenied
	}
	session, err := newProjectSession(a.Project, token)
	if err != nil {
		return "", err
	}
	id, err := s.findHumanReleaseReplay(ctx, session, a)
	if !errors.Is(err, sql.ErrNoRows) {
		return id, err
	}
	snapshot, err := s.snapshotPublishedRelease(ctx, &a)
	if err != nil {
		return "", err
	}
	a.session = &session
	return s.AdmitSnapshot(ctx, a, snapshot)
}

func (s *Store) findHumanReleaseReplay(ctx context.Context, session projectSession, a Admission) (string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	a.Caller, err = authorizeHumanRelease(ctx, tx, session, a.Release)
	if err != nil {
		return "", err
	}
	return findAdmission(ctx, tx, a, a.ExpectedSnapshotDigest)
}

func (s *Store) snapshotPublishedRelease(ctx context.Context, a *Admission) (release.Snapshot, error) {
	var document, expected string
	err := s.db.QueryRowContext(ctx, `SELECT compose,digest FROM (`+visibleReleases+`)
 WHERE EXISTS (SELECT 1 FROM targets WHERE project=? AND id=?) ORDER BY precedence LIMIT 1`,
		a.Project, a.Target, a.Release, a.Project, a.Release, a.Project, a.Target).Scan(&document, &expected)
	if err != nil {
		return release.Snapshot{}, err
	}
	artifact, err := release.Compile(ctx, []byte(document), nil, nil, nil)
	if err != nil {
		return release.Snapshot{}, err
	}
	if artifact.Digest() != expected {
		return release.Snapshot{}, errors.New("stored release digest mismatch")
	}
	return s.bindReleaseSnapshot(ctx, a, artifact)
}

func (s *Store) bindReleaseSnapshot(ctx context.Context, a *Admission, artifact release.Release) (release.Snapshot, error) {
	version, bindings, err := targetSecrets(ctx, s.db, a.Project, a.Target)
	if err != nil {
		return release.Snapshot{}, err
	}
	if err := requireTemplateSecrets(bindings, a.templateSecretServices); err != nil {
		return release.Snapshot{}, err
	}
	a.targetConfigVersion = version
	snapshot, err := artifact.Bind(release.Binding{Target: a.Target, ConfigVersion: version, HealthTimeout: 2 * time.Minute,
		Secrets: bindings.Secrets, SecretServices: bindings.Services, ExternalNode: a.ExternalNode, ForceRecreate: a.forceRecreate})
	if err != nil {
		return release.Snapshot{}, errors.Join(ErrInvalid, err)
	}
	return snapshot, nil
}

// Check current authority on new or unconfirmed offers. Work already running
// retains its assigned execution identity and can still report its outcome.
const credentialActive = `((d.credential_id IS NULL OR EXISTS (
 SELECT 1 FROM service_credentials c WHERE c.id=d.credential_id AND c.revoked=0 AND c.expires_ms>?))
 AND (d.human_submitter IS NULL OR EXISTS (SELECT 1 FROM platform_admins WHERE user_id=d.human_submitter)
 OR (d.release_id NOT GLOB 'console-template-*' AND EXISTS (SELECT 1 FROM project_members m WHERE m.project=d.project AND m.user_id=d.human_submitter
  AND m.role IN ('project_admin','operator')))))`
