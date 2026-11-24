package storage

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/maxtanforge/moori/internal/core/nodeops"
)

var webhookKey = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`)

type ContainerWebhookBinding struct {
	Node       string `json:"node"`
	Generation int64  `json:"generation"`
	Container  string `json:"container"`
	Repository string `json:"repository"`
	Project    string `json:"project,omitempty"`
	Target     string `json:"target,omitempty"`
	RegistryID string `json:"registry_id,omitempty"`
}

type ContainerWebhookAccess struct {
	ID    string `json:"id"`
	Token string `json:"token"`
}

type ContainerWebhook struct {
	ID           string `json:"id"`
	RegistryName string `json:"registry_name,omitempty"`
	ContainerWebhookBinding
}

func (s *Store) ListContainerWebhooks(ctx context.Context, session, after string) (CatalogPage[ContainerWebhook], error) {
	page := CatalogPage[ContainerWebhook]{Items: []ContainerWebhook{}}
	if after != "" && !webhookKey.MatchString(after) {
		return page, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return page, err
	}
	defer tx.Rollback()
	if _, err := platformActor(ctx, tx, session); err != nil {
		return page, err
	}
	const limit = 100
	rows, err := tx.QueryContext(ctx, `SELECT h.id,h.agent,h.generation,h.name,h.repository,h.project,h.target,h.registry_id,COALESCE(r.name,'')
 FROM container_webhooks h LEFT JOIN registries r ON r.id=h.registry_id
 WHERE h.revoked=0 AND h.id>? ORDER BY h.id LIMIT ?`, after, limit+1)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		if len(page.Items) == limit {
			page.Next = page.Items[limit-1].ID
			break
		}
		var item ContainerWebhook
		if err := rows.Scan(&item.ID, &item.Node, &item.Generation, &item.Container, &item.Repository, &item.Project, &item.Target, &item.RegistryID, &item.RegistryName); err != nil {
			return page, err
		}
		page.Items = append(page.Items, item)
	}
	if err := rows.Err(); err != nil {
		return page, err
	}
	return page, rows.Close()
}

func (b ContainerWebhookBinding) operation(image string) nodeops.Request {
	return nodeops.Request{Action: nodeops.ContainerRelease, Name: b.Container, Image: image,
		RegistryID: b.RegistryID}
}

func (s *Store) CreateContainerWebhook(ctx context.Context, session string, binding ContainerWebhookBinding) (ContainerWebhookAccess, error) {
	var access ContainerWebhookAccess
	probe := binding.Repository + "@sha256:" + strings.Repeat("0", 64)
	validation := binding.operation(probe)
	// Keep validating metadata accepted from older clients.
	validation.Project, validation.Target = binding.Project, binding.Target
	if binding.Node == "" || len(binding.Node) > 128 || binding.Generation <= 0 ||
		!nodeops.ContainerRepository(binding.Repository) || validation.Validate() != nil {
		return access, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return access, err
	}
	defer tx.Rollback()
	actor, err := platformActor(ctx, tx, session)
	if err != nil {
		return access, err
	}
	if err := requireRecoveryComplete(ctx, tx); err != nil {
		return access, err
	}
	var active bool
	if err := tx.QueryRowContext(ctx, `SELECT `+activeOperationNode, binding.Node, binding.Generation, time.Now().UnixMilli()).Scan(&active); err != nil {
		return access, err
	}
	if !active {
		return access, ErrConflict
	}
	if err := authorizeWebhookRegistry(ctx, tx, binding, probe); err != nil {
		return access, err
	}
	access = ContainerWebhookAccess{ID: "hook-" + rand.Text(), Token: rand.Text()}
	hash := sha256.Sum256([]byte(access.Token))
	res, err := tx.ExecContext(ctx, `INSERT INTO container_webhooks(id,token_hash,actor,agent,generation,name,repository,project,target,registry_id)
 VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT DO NOTHING`, access.ID, hash[:], actor, binding.Node, binding.Generation, binding.Container,
		binding.Repository, binding.Project, binding.Target, binding.RegistryID)
	if err := expectOne(res, err, ErrConflict); err != nil {
		return ContainerWebhookAccess{}, err
	}
	if err := addProjectAudit(ctx, tx, "", "webhook.created", actor, access.ID, binding.Node+" / "+binding.Container); err != nil {
		return ContainerWebhookAccess{}, err
	}
	if err := tx.Commit(); err != nil {
		return ContainerWebhookAccess{}, err
	}
	return access, nil
}

func (s *Store) RevokeContainerWebhook(ctx context.Context, session, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	actor, err := platformActor(ctx, tx, session)
	if err != nil {
		return err
	}
	var agent, name string
	var revoked bool
	if err := tx.QueryRowContext(ctx, `SELECT agent,name,revoked FROM container_webhooks WHERE id=?`, id).Scan(&agent, &name, &revoked); err != nil {
		return err
	}
	if revoked {
		return tx.Commit()
	}
	res, err := tx.ExecContext(ctx, `UPDATE container_webhooks SET revoked=1 WHERE id=?`, id)
	if err := expectOne(res, err, sql.ErrNoRows); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE node_operations SET state='failed',error='Container webhook was revoked.'
 WHERE state='queued' AND id IN (SELECT operation FROM container_webhook_deliveries WHERE webhook=?)`, id); err != nil {
		return err
	}
	if err := addProjectAudit(ctx, tx, "", "webhook.revoked", actor, id, agent+" / "+name); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) QueueContainerWebhook(ctx context.Context, id, token, key, image string, now time.Time) (nodeops.Operation, error) {
	var op nodeops.Operation
	parsed, err := nodeops.ContainerReleaseImage(image)
	if err != nil || !webhookKey.MatchString(key) {
		return op, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return op, err
	}
	defer tx.Rollback()
	binding, actor, err := findContainerWebhook(ctx, tx, id, token)
	if err != nil {
		return op, err
	}
	if parsed.Host+"/"+parsed.Repository != binding.Repository {
		return op, ErrAccessDenied
	}
	if err := retireExpiredNodeOperations(ctx, tx, binding.Node, now); err != nil {
		return op, err
	}
	var previous string
	err = tx.QueryRowContext(ctx, `SELECT q.id,q.agent,q.generation,q.action,q.state,q.error,d.image
 FROM container_webhook_deliveries d JOIN node_operations q ON q.id=d.operation WHERE d.webhook=? AND d.delivery_key=?`, id, key).
		Scan(&op.ID, &op.Agent, &op.Generation, &op.Action, &op.State, &op.Error, &previous)
	if err == nil {
		if previous != image {
			return nodeops.Operation{}, ErrConflict
		}
		return op, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nodeops.Operation{}, err
	}
	if err := authorizeWebhookRegistry(ctx, tx, binding, image); err != nil {
		return nodeops.Operation{}, err
	}
	op, err = s.queueNodeOperation(ctx, tx, actor, binding.Node, binding.Generation, binding.operation(image), now)
	if err != nil {
		return op, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO container_webhook_deliveries(webhook,delivery_key,image,operation) VALUES(?,?,?,?)`, id, key, image, op.ID); err != nil {
		return nodeops.Operation{}, err
	}
	return op, tx.Commit()
}

func (s *Store) ContainerWebhookOperation(ctx context.Context, id, token, operation string, now time.Time) (nodeops.Operation, error) {
	var op nodeops.Operation
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return op, err
	}
	defer tx.Rollback()
	binding, _, err := findContainerWebhook(ctx, tx, id, token)
	if err != nil {
		return op, err
	}
	if err := retireExpiredNodeOperations(ctx, tx, binding.Node, now); err != nil {
		return op, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT q.id,q.agent,q.generation,q.action,q.state,q.error
 FROM container_webhook_deliveries d JOIN node_operations q ON q.id=d.operation WHERE d.webhook=? AND d.operation=?`, id, operation).
		Scan(&op.ID, &op.Agent, &op.Generation, &op.Action, &op.State, &op.Error); err != nil {
		return op, err
	}
	return op, tx.Commit()
}

func findContainerWebhook(ctx context.Context, q queryRower, id, token string) (ContainerWebhookBinding, string, error) {
	var binding ContainerWebhookBinding
	var actor string
	hash := sha256.Sum256([]byte(token))
	err := q.QueryRowContext(ctx, `SELECT actor,agent,generation,name,repository,project,target,registry_id
 FROM container_webhooks WHERE id=? AND token_hash=? AND revoked=0
 AND EXISTS(SELECT 1 FROM platform_admins WHERE user_id=actor)`, id, hash[:]).
		Scan(&actor, &binding.Node, &binding.Generation, &binding.Container, &binding.Repository, &binding.Project, &binding.Target, &binding.RegistryID)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrForbidden
	}
	return binding, actor, err
}

func authorizeWebhookRegistry(ctx context.Context, tx *sql.Tx, b ContainerWebhookBinding, value string) error {
	image, err := nodeops.ContainerReleaseImage(value)
	if err != nil {
		return err
	}
	_, err = selectRegistry(ctx, tx, b.RegistryID, image)
	return err
}
