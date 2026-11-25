package storage

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/maxtanforge/moori/internal/core/nodeops"
)

// The receipt ties a CI operation to a live token and the original placement.
// Other node operations keep their existing platform administrator check.
const nodeOperationPrincipalAllowed = `((substr(q.actor,1,3)<>'ci:' AND EXISTS(SELECT 1 FROM platform_admins WHERE user_id=q.actor))
 OR (q.action='container.release' AND EXISTS (
 SELECT 1 FROM ci_container_deliveries d JOIN service_credentials c ON c.id=d.credential
 JOIN ci_container_targets b ON b.target=d.target JOIN targets t ON t.id=b.target
 JOIN target_assignments a ON a.target=t.id
 JOIN ci_credential_scopes g ON g.credential=c.id AND g.project=t.project AND g.target=t.id
 WHERE d.operation=q.id AND q.actor='ci:'||c.id
 AND c.revoked=0 AND c.expires_ms>?
 AND a.agent=b.agent AND a.generation=b.generation AND q.agent=b.agent AND q.generation=b.generation)))`

type CIPipelineTarget struct {
	Project    string `json:"project"`
	Target     string `json:"target"`
	Kind       string `json:"kind"`
	Node       string `json:"node"`
	Generation int64  `json:"generation"`
	Container  string `json:"container,omitempty"`
	Repository string `json:"repository,omitempty"`
	RegistryID string `json:"registry_id,omitempty"`
}

func (s *Store) RegisterContainerTarget(ctx context.Context, project, target, agent string, generation int64, name, repository, registryID string) error {
	b := CIPipelineTarget{Project: project, Target: target, Node: agent, Generation: generation, Container: name, Repository: repository, RegistryID: registryID}
	return s.createPipelineTarget(ctx, "", &b)
}

func (s *Store) CreateCIPipelineTarget(ctx context.Context, session string, b CIPipelineTarget) (CIPipelineTarget, error) {
	if session == "" {
		return CIPipelineTarget{}, ErrForbidden
	}
	err := s.createPipelineTarget(ctx, session, &b)
	return b, err
}

func (s *Store) createPipelineTarget(ctx context.Context, session string, b *CIPipelineTarget) error {
	if b.Kind == "" {
		b.Kind = "container"
	}
	if b.Kind != "container" && b.Kind != "compose" {
		return ErrInvalid
	}
	if !webhookKey.MatchString(b.Project) || !webhookKey.MatchString(b.Target) || b.Node == "" || len(b.Node) > 128 ||
		b.Generation <= 0 {
		return ErrInvalid
	}
	probe := b.Repository + "@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if b.Kind == "container" && (!nodeops.ContainerRepository(b.Repository) || b.binding().operation(probe).Validate() != nil) {
		return ErrInvalid
	}
	if b.Kind == "compose" && (b.Container != "" || b.Repository != "" || b.RegistryID != "") {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	actor := "local"
	if session != "" {
		actor, err = platformActor(ctx, tx, session)
		if err != nil {
			return err
		}
	}
	if err = requireRecoveryComplete(ctx, tx); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO targets(id,project) SELECT ?,? WHERE EXISTS(SELECT 1 FROM agent_certificates WHERE agent=? AND generation=?) ON CONFLICT DO NOTHING`, b.Target, b.Project, b.Node, b.Generation)
	if err = expectOne(res, err, ErrConflict); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO target_assignments(target,agent,generation) VALUES(?,?,?)`, b.Target, b.Node, b.Generation); err != nil {
		return err
	}
	if b.Kind == "container" {
		if err := authorizeWebhookRegistry(ctx, tx, b.binding(), probe); err != nil {
			return err
		}
		res, err = tx.ExecContext(ctx, `INSERT INTO ci_container_targets(target,agent,generation,name,repository,registry_id) VALUES(?,?,?,?,?,?) ON CONFLICT DO NOTHING`, b.Target, b.Node, b.Generation, b.Container, b.Repository, b.RegistryID)
		if err = expectOne(res, err, ErrConflict); err != nil {
			return err
		}
	}
	if err = addProjectAudit(ctx, tx, b.Project, "ci_target.created", actor, b.Target, b.Node+" / "+b.Container); err != nil {
		return err
	}
	return tx.Commit()
}

func (b CIPipelineTarget) binding() ContainerWebhookBinding {
	return ContainerWebhookBinding{Node: b.Node, Generation: b.Generation, Container: b.Container, Repository: b.Repository, RegistryID: b.RegistryID}
}

func (s *Store) QueueCIContainerDeployment(ctx context.Context, token, project, target, key, image string, now time.Time) (nodeops.Operation, error) {
	var op nodeops.Operation
	parsed, err := nodeops.ContainerReleaseImage(image)
	if err != nil || !webhookKey.MatchString(key) {
		return op, ErrInvalid
	}
	hash, err := credentialHash(token)
	if err != nil {
		return op, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return op, err
	}
	defer tx.Rollback()
	credential, err := findCredential(ctx, tx, hash, project, target)
	if err != nil {
		return op, err
	}
	b := CIPipelineTarget{Project: project, Target: target}
	var placementOK bool
	err = tx.QueryRowContext(ctx, `SELECT b.agent,b.generation,b.name,b.repository,b.registry_id,
 EXISTS(SELECT 1 FROM target_assignments a WHERE a.target=b.target AND a.agent=b.agent AND a.generation=b.generation)
 FROM ci_container_targets b JOIN targets t ON t.id=b.target WHERE b.target=?`, target).
		Scan(&b.Node, &b.Generation, &b.Container, &b.Repository, &b.RegistryID, &placementOK)
	if err != nil {
		return op, err
	}
	if parsed.Host+"/"+parsed.Repository != b.Repository {
		return op, ErrAccessDenied
	}
	if err = retireExpiredNodeOperations(ctx, tx, b.Node, now); err != nil {
		return op, err
	}
	var previous string
	err = tx.QueryRowContext(ctx, `SELECT q.id,q.agent,q.generation,q.action,q.state,q.error,d.image
 FROM ci_container_deliveries d JOIN node_operations q ON q.id=d.operation WHERE d.credential=? AND d.target=? AND d.delivery_key=?`, credential, target, key).
		Scan(&op.ID, &op.Agent, &op.Generation, &op.Action, &op.State, &op.Error, &previous)
	if err == nil {
		if previous != image {
			return nodeops.Operation{}, ErrConflict
		}
		return op, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return op, err
	}
	if !placementOK {
		return op, ErrConflict
	}
	binding := b.binding()
	if err = authorizeWebhookRegistry(ctx, tx, binding, image); err != nil {
		return op, err
	}
	op, err = s.queueNodeOperation(ctx, tx, "ci:"+credential, b.Node, b.Generation, binding.operation(image), now)
	if err != nil {
		return op, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO ci_container_deliveries(credential,delivery_key,target,image,operation) VALUES(?,?,?,?,?)`, credential, key, target, image, op.ID); err != nil {
		return op, err
	}
	return op, tx.Commit()
}

func (s *Store) ReadCIContainerDeployment(ctx context.Context, token, id string, now time.Time) (nodeops.Operation, error) {
	var op nodeops.Operation
	hash, err := credentialHash(token)
	if err != nil {
		return op, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return op, err
	}
	defer tx.Rollback()
	var credential string
	err = tx.QueryRowContext(ctx, `SELECT id FROM service_credentials WHERE token_hash=? AND revoked=0 AND expires_ms>?`, hash[:], now.UnixMilli()).Scan(&credential)
	if errors.Is(err, sql.ErrNoRows) {
		return op, ErrForbidden
	}
	if err != nil {
		return op, err
	}
	var node string
	err = tx.QueryRowContext(ctx, `SELECT q.agent FROM ci_container_deliveries d JOIN node_operations q ON q.id=d.operation WHERE d.credential=? AND d.operation=?`, credential, id).Scan(&node)
	if err != nil {
		return op, err
	}
	if err = retireExpiredNodeOperations(ctx, tx, node, now); err != nil {
		return op, err
	}
	err = tx.QueryRowContext(ctx, `SELECT id,agent,generation,action,state,error FROM node_operations WHERE id=?`, id).Scan(&op.ID, &op.Agent, &op.Generation, &op.Action, &op.State, &op.Error)
	if err != nil {
		return op, err
	}
	return op, tx.Commit()
}
