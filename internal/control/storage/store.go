// Package storage persists control-plane deployment decisions.
package storage

import (
	"context"
	"crypto/rand"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/maxtanforge/moori/internal/core/deployment"
	"github.com/maxtanforge/moori/internal/core/release"
	"github.com/maxtanforge/moori/internal/platform/sqlite"
)

var (
	ErrConflict   = errors.New("request conflicts with stored data")
	ErrTargetBusy = errors.New("target already has an active execution")
	ErrState      = errors.New("deployment state changed or transition is not permitted")
	ErrInvalid    = errors.New("invalid deployment input")
	ErrForbidden  = errors.New("principal is not granted this deployment action")
	ErrRecovery   = errors.New("control database is in recovery mode")
	// ErrRegistryHostBound reports a host change on a registry whose stored
	// credential is cryptographically bound to the current address.
	ErrRegistryHostBound = errors.New("registry credential is bound to its registered host")
)

//go:embed schema.sql
var schema string

//go:embed agent_identity_history.sql
var agentIdentityHistory string

//go:embed indexes.sql
var indexes string

type Store struct {
	db              *sql.DB
	agentSessionsMu sync.Mutex
	agentSessions   map[*agentSession]struct{}
	editResults     editConfigResults
	agentIssueKey   [32]byte
	composeDraftKey [32]byte
}

// The initial schema has its own identity, distinct from the retired migration chain.
const controlApplicationID = 0x4d4f4f63

// controlSchemaVersion counts the migrations applied after the initial schema.
const controlSchemaVersion = 3

func Open(ctx context.Context, path string) (*Store, error) {
	db, err := sqlite.Open(ctx, path, controlApplicationID, schema, indexes, agentIdentityHistory)
	if err != nil {
		return nil, err
	}
	s := &Store{db: db, agentSessions: make(map[*agentSession]struct{})}
	if _, err := rand.Read(s.agentIssueKey[:]); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := rand.Read(s.composeDraftKey[:]); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) RegisterTarget(ctx context.Context, project, id string) error {
	if project == "" || id == "" {
		return ErrInvalid
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO targets(id,project) SELECT ?,? WHERE NOT `+recoveryAwaitingRelease, id, project)
	return expectOne(res, err, ErrRecovery)
}

type Admission struct {
	ID, Project, Caller, Key, Target, Release string
	ExternalNode                              *release.NodeIdentity
	ExpectedSnapshotDigest                    string
	rollbackOf, rollbackReason                string
	targetConfigVersion                       string
	session                                   *projectSession
	consoleArtifact                           *release.Release
	composeBase                               string
	composeNode                               release.NodeIdentity
	forceRecreate                             bool
	templateSecretServices                    map[string][]string
}

func (s *Store) AdmitSnapshot(ctx context.Context, a Admission, snapshot release.Snapshot) (string, error) {
	return s.admitSnapshot(ctx, a, snapshot, nil, "")
}

func (s *Store) admitSnapshot(ctx context.Context, a Admission, snapshot release.Snapshot, hash *[32]byte, principal string) (string, error) {
	if a.ID == "" || a.Project == "" || (hash == nil && a.session == nil && (a.Caller == "" || strings.HasPrefix(a.Caller, "service:") || strings.HasPrefix(a.Caller, "integration:"))) || a.Key == "" || a.Target == "" ||
		a.Release == "" || snapshot.Digest() == "" || snapshot.Target() != a.Target {
		return "", ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var credential, humanSubmitter any
	if a.session != nil {
		if a.session.project != a.Project {
			return "", ErrAccessDenied
		}
		a.Caller, err = authorizeHumanRelease(ctx, tx, *a.session, a.Release)
		if err != nil {
			return "", err
		}
		humanSubmitter = a.Caller
	}
	if hash != nil {
		if strings.HasPrefix(a.Release, consoleTemplateReleasePrefix) {
			return "", ErrForbidden
		}
		credentialID, err := findCredential(ctx, tx, *hash, a.Project, a.Target)
		if err != nil {
			return "", err
		}
		credential = credentialID
		a.Caller = "service:" + credentialID
		if principal != "" {
			a.Caller = principal
		} else {
			var primary string
			if err := tx.QueryRowContext(ctx, `SELECT target FROM service_credentials WHERE id=?`, credentialID).Scan(&primary); err != nil {
				return "", err
			}
			// Keep the original idempotency namespace for existing primary grants.
			// HTTP request keys cannot contain '/', so this cannot alias those keys.
			if primary != a.Target {
				a.Key = "target/" + a.Target + "/" + a.Key
			}
		}
	}
	if _, err := targetDispatchEpoch(ctx, tx, a.Target); err != nil {
		return "", err
	}
	matchDigest := snapshot.Digest()
	if a.session != nil && a.rollbackOf == "" {
		matchDigest = ""
	}
	if a.ExpectedSnapshotDigest != "" {
		matchDigest = a.ExpectedSnapshotDigest
	}
	id, err := findAdmission(ctx, tx, a, matchDigest)
	switch {
	case err == nil:
		return id, tx.Commit()
	case !errors.Is(err, sql.ErrNoRows):
		return "", err
	}
	if matchDigest != "" && matchDigest != snapshot.Digest() {
		return "", ErrConflict
	}
	if a.composeBase != "" {
		if err := requireComposeBaseline(ctx, tx, a.Target, a.composeBase, a.composeNode, true); err != nil {
			return "", err
		}
	}
	if a.rollbackOf != "" {
		if err := validateRollbackSource(ctx, tx, a, snapshot, credential); err != nil {
			return "", err
		}
	}
	if err := requireExternalSnapshotNode(ctx, tx, a.Project, snapshot); err != nil {
		return "", err
	}
	if a.targetConfigVersion != "" {
		version, _, err := targetSecrets(ctx, tx, a.Project, a.Target)
		if err != nil {
			return "", err
		}
		if version != a.targetConfigVersion {
			return "", ErrConflict
		}
	}
	if a.consoleArtifact != nil {
		if err := insertTargetRelease(ctx, tx, a.Project, a.Target, a.Release, *a.consoleArtifact); err != nil {
			return "", err
		}
	}
	var published bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM ("+visibleReleases+") WHERE digest=?)", a.Project, a.Target, a.Release, a.Project, a.Release, snapshot.ReleaseDigest()).Scan(&published); err != nil {
		return "", err
	}
	if !published {
		return "", ErrConflict
	}
	if err := requireSecretVersions(ctx, tx, a.Project, a.Target, snapshot.SecretReferences()); err != nil {
		return "", err
	}
	var containerTarget bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM ci_container_targets ct WHERE ct.target=targets.id)
 FROM targets WHERE project=? AND id=?`, a.Project, a.Target).Scan(&containerTarget); err != nil {
		return "", err
	}
	if containerTarget {
		return "", ErrInvalid
	}
	initial := deployment.Queued
	if _, err := tx.ExecContext(ctx, `INSERT INTO deployments(id,project,caller,request_key,target,release_id,digest,state,credential_id,rollback_of,rollback_reason,human_submitter)
 VALUES (?,?,?,?,?,?,?,?,?,nullif(?,''),?,?)`, a.ID, a.Project, a.Caller, a.Key, a.Target, a.Release, snapshot.Digest(), initial, credential, a.rollbackOf, a.rollbackReason, humanSubmitter); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO snapshots(deployment_id,document) VALUES (?,?)", a.ID, snapshot.JSON()); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO deployment_audit(deployment_id,state,reason) VALUES (?,?,?)", a.ID, initial, a.rollbackReason); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return a.ID, nil
}

// A browser requests a published release by identity. Pass an empty digest
// when replay should use the originally accepted immutable snapshot.
func findAdmission(ctx context.Context, q queryRower, a Admission, matchDigest string) (string, error) {
	var id, target, releaseID, digest, rollbackOf, rollbackReason string
	err := q.QueryRowContext(ctx, `SELECT id,target,release_id,digest,coalesce(rollback_of,''),rollback_reason FROM deployments
 WHERE project=? AND caller=? AND request_key=?`, a.Project, a.Caller, a.Key).Scan(&id, &target, &releaseID, &digest, &rollbackOf, &rollbackReason)
	if err != nil {
		return "", err
	}
	if target != a.Target || releaseID != a.Release || rollbackOf != a.rollbackOf || rollbackReason != a.rollbackReason ||
		matchDigest != "" && matchDigest != digest {
		return "", ErrConflict
	}
	if a.ExternalNode != nil {
		var matched bool
		if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM snapshots
 WHERE deployment_id=? AND json_extract(document,'$.external_node.agent')=?
 AND json_extract(document,'$.external_node.generation')=?)`, id, a.ExternalNode.Agent, a.ExternalNode.Generation).Scan(&matched); err != nil {
			return "", err
		}
		if !matched {
			return "", ErrConflict
		}
	}
	return id, nil
}

type View struct {
	State                                deployment.State
	Target, Release, Digest, LastSuccess string
	Execution, Agent                     string
	Generation                           int64
	RollbackOf                           string
}

func (s *Store) Get(ctx context.Context, id string) (View, error) {
	var v View
	err := s.db.QueryRowContext(ctx, `SELECT d.state,d.target,d.release_id,d.digest,t.last_success,
 coalesce(d.execution_id,''),coalesce(d.agent,''),coalesce(d.generation,0),coalesce(d.rollback_of,'')
 FROM deployments d JOIN targets t ON t.id=d.target WHERE d.id=?`, id).
		Scan(&v.State, &v.Target, &v.Release, &v.Digest, &v.LastSuccess, &v.Execution, &v.Agent, &v.Generation, &v.RollbackOf)
	return v, err
}

func (s *Store) Claim(ctx context.Context, id, execution, agent string, generation int64, expires time.Time) error {
	if id == "" || execution == "" || agent == "" || generation <= 0 || expires.UnixMilli() <= 0 {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var target, boundExecution, boundAgent string
	var boundGeneration, boundExpiry int64
	var state deployment.State
	if err := tx.QueryRowContext(ctx, `SELECT target,state,coalesce(execution_id,''),coalesce(agent,''),
 coalesce(generation,0),coalesce(expires_ms,0) FROM deployments WHERE id=?`, id).
		Scan(&target, &state, &boundExecution, &boundAgent, &boundGeneration, &boundExpiry); err != nil {
		return err
	}
	epoch, err := targetDispatchEpoch(ctx, tx, target)
	if err != nil {
		return err
	}
	if epoch != "" {
		var assigned bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM target_assignments WHERE target=? AND agent=? AND generation=?)`,
			target, agent, generation).Scan(&assigned); err != nil {
			return err
		}
		if !assigned {
			return ErrConflict
		}
	}
	if boundExecution != "" {
		if boundExecution == execution && boundAgent == agent && boundGeneration == generation && boundExpiry == expires.UnixMilli() {
			return tx.Commit()
		}
		return ErrConflict
	}
	if !deployment.CanTransition(state, deployment.Dispatching) {
		return ErrState
	}
	running, err := nodeMutationInProgress(ctx, tx, agent, time.Now())
	if err != nil {
		return err
	}
	if running {
		return ErrTargetBusy
	}
	var matches bool
	if err := tx.QueryRowContext(ctx, `SELECT NOT `+externalResourcesInSnapshot+` OR (
 `+externalAssignmentMatches+` AND json_extract(p.document,'$.external_node.agent')=?
 AND json_extract(p.document,'$.external_node.generation')=?)
 FROM deployments d JOIN snapshots p ON p.deployment_id=d.id WHERE d.id=?`, agent, generation, id).Scan(&matches); err != nil {
		return err
	}
	if !matches {
		return ErrConflict
	}
	var authorized bool
	if err := tx.QueryRowContext(ctx, "SELECT "+credentialActive+" FROM deployments d WHERE d.id=?", time.Now().UnixMilli(), id).Scan(&authorized); err != nil {
		return err
	}
	if !authorized {
		return ErrForbidden
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO target_slots(target,deployment_id) VALUES (?,?)
 ON CONFLICT(target) DO NOTHING`, target, id)
	if err := expectOne(res, err, ErrTargetBusy); err != nil {
		return err
	}
	res, err = tx.ExecContext(ctx, `UPDATE deployments SET state=?,execution_id=?,agent=?,generation=?,expires_ms=?,dispatch_epoch=?
 WHERE id=? AND state=?`, deployment.Dispatching, execution, agent, generation, expires.UnixMilli(), epoch, id, state)
	if err := expectOne(res, err, ErrState); err != nil {
		return err
	}
	if err := audit(ctx, tx, id, deployment.Dispatching); err != nil {
		return err
	}
	return tx.Commit()
}

func expectOne(res sql.Result, err, missing error) error {
	if err != nil {
		return err
	}
	count, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("%w: expected one affected row, got %d", missing, count)
	}
	return nil
}

func audit(ctx context.Context, tx *sql.Tx, id string, state deployment.State) error {
	_, err := tx.ExecContext(ctx, "INSERT INTO deployment_audit(deployment_id,state) VALUES (?,?)", id, state)
	return err
}
