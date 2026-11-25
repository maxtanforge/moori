package storage

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/maxtanforge/moori/internal/core/nodeops"
)

var (
	ErrNodeOperationsUnavailable = errors.New("this node has not recently advertised Docker management")
	ErrNodeOperationsBusy        = errors.New("too many Docker operations are waiting for this node")
)

const nodeCapabilityTTL = 2 * time.Minute
const nodeOperationStartWindow = 15 * time.Second
const nodeOperationQueueTTL = 3 * time.Minute

// A node-wide mutation could affect restored targets whose release no longer
// matches the node's generation. Hold that node until placement is repaired.
const restoredNodeMutationAllowed = `NOT EXISTS (
 SELECT 1 FROM recovery_state rs
 JOIN targets t ON t.restore_epoch=rs.epoch
 JOIN target_assignments a ON a.target=t.id
 WHERE a.agent=q.agent AND a.generation<>q.generation
 )`

// A node mutation can affect historic work anywhere on that Docker host,
// including deployments absent from the backup. Current-epoch work already
// has its own slot and lifecycle, so stale inventory for it is immaterial.
const nodeHasActiveRecoveryEvidence = `EXISTS (` + recoveryObservations + `
 SELECT 1 FROM observed o WHERE o.agent=q.agent AND ` + latestActiveRecoveryObservation + `
 AND NOT EXISTS (SELECT 1 FROM deployments d WHERE d.execution_id=o.execution_id
  AND d.agent=o.agent AND d.generation=o.generation AND d.dispatch_epoch=o.epoch)
 )`

const activeOperationNode = `EXISTS (
  SELECT 1 FROM agent_certificates a WHERE a.agent=? AND a.generation=?
  AND NOT EXISTS (
    SELECT 1 FROM agent_credentials c WHERE c.agent=a.agent AND c.generation=a.generation AND (c.revoked=1 OR c.expires_ms<=?)
  )
)`

const privateNodeOperationActions = `('container.replace','container.create','compose.source-create','compose.source-preview','compose.source-save','compose.source-deploy')`

// Pre-restore operations must never be resent. A claimed mutation can still
// be running on Docker, so keep its deadline as a node-wide fence.
const retireRestoredNodeOperations = `DELETE FROM node_operation_capabilities WHERE EXISTS (SELECT 1 FROM recovery_state);
 UPDATE node_operations SET restore_fenced=(state='running' AND mutation=1),
 state=CASE WHEN state='running' AND mutation=1 THEN 'uncertain' ELSE 'failed' END,
 request=CASE WHEN action IN ` + privateNodeOperationActions + ` THEN '{}' ELSE request END,
 error=CASE WHEN state='running' AND mutation=1 THEN 'The result was lost during database restore. Refresh Docker resources before retrying.'
            ELSE 'The operation was invalidated by database restore.' END
 WHERE state IN ('queued','running') AND EXISTS (SELECT 1 FROM recovery_state);`

func (s *Store) RecordNodeOperationCapability(ctx context.Context, agent string, generation int64, now time.Time) error {
	if agent == "" || generation <= 0 || now.IsZero() {
		return ErrInvalid
	}
	ms := now.UnixMilli()
	_, err := s.db.ExecContext(ctx, `INSERT INTO node_operation_capabilities(agent,generation,seen_ms)
 SELECT ?,?,? WHERE `+activeOperationNode+`
 ON CONFLICT(agent) DO UPDATE SET generation=excluded.generation,seen_ms=excluded.seen_ms
 WHERE excluded.generation<>node_operation_capabilities.generation OR node_operation_capabilities.seen_ms<?`,
		agent, generation, ms, agent, generation, ms, now.Add(-30*time.Second).UnixMilli())
	return err
}

func (s *Store) NodeOperationAvailable(ctx context.Context, agent string, generation int64, now time.Time) (bool, error) {
	return nodeOperationAvailable(ctx, s.db, agent, generation, now)
}

func nodeOperationAvailable(ctx context.Context, q queryRower, agent string, generation int64, now time.Time) (bool, error) {
	var ready bool
	err := q.QueryRowContext(ctx, `SELECT `+activeOperationNode+`
 AND EXISTS (SELECT 1 FROM node_operation_capabilities WHERE agent=? AND generation=? AND seen_ms>?)`,
		agent, generation, now.UnixMilli(), agent, generation, now.Add(-nodeCapabilityTTL).UnixMilli()).Scan(&ready)
	return ready, err
}

func (s *Store) QueueNodeOperation(ctx context.Context, token, agent string, generation int64, req nodeops.Request, now time.Time) (nodeops.Operation, error) {
	return s.admitNodeOperation(ctx, token, agent, generation, req, now, false)
}

// StartLocalNodeOperation claims the submitted job if it is next in line.
// Contention leaves it in the normal queue.
func (s *Store) StartLocalNodeOperation(ctx context.Context, token string, generation int64, req nodeops.Request, now time.Time) (nodeops.Operation, error) {
	if !req.Action.LocalDirect() {
		return nodeops.Operation{}, ErrInvalid
	}
	return s.admitNodeOperation(ctx, token, LocalExecutorID, generation, req, now, true)
}

func (s *Store) admitNodeOperation(ctx context.Context, token, agent string, generation int64, req nodeops.Request, now time.Time, immediate bool) (nodeops.Operation, error) {
	if req.Action == nodeops.ContainerRelease {
		return nodeops.Operation{}, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nodeops.Operation{}, err
	}
	defer tx.Rollback()
	actor, err := platformActor(ctx, tx, token)
	if err != nil {
		return nodeops.Operation{}, err
	}
	op, err := s.queueNodeOperation(ctx, tx, actor, agent, generation, req, now)
	if err != nil {
		return nodeops.Operation{}, err
	}
	if immediate {
		_, _, err := s.claimNodeOperation(ctx, tx, agent, generation, op.ID, now)
		if err == nil {
			op.State = nodeops.Running
		} else if !errors.Is(err, sql.ErrNoRows) {
			return op, err
		}
	}
	return op, tx.Commit()
}

func (s *Store) queueNodeOperation(ctx context.Context, tx *sql.Tx, actor, agent string, generation int64, req nodeops.Request, now time.Time) (nodeops.Operation, error) {
	var op nodeops.Operation
	if agent == "" || len(agent) > 128 || generation <= 0 || req.Operation != "" ||
		req.Generation != 0 && req.Generation != generation || now.IsZero() || req.Validate() != nil {
		return op, ErrInvalid
	}
	req.Generation = generation
	request, err := json.Marshal(req)
	if err != nil {
		return op, err
	}
	if err := requireRecoveryComplete(ctx, tx); err != nil {
		return op, err
	}
	ready, err := nodeOperationAvailable(ctx, tx, agent, generation, now)
	if err != nil {
		return op, err
	}
	if !ready {
		return op, ErrNodeOperationsUnavailable
	}
	if req.Action.Mutates() {
		var placementReady, oldWorkActive bool
		if err := tx.QueryRowContext(ctx, "SELECT "+restoredNodeMutationAllowed+","+nodeHasActiveRecoveryEvidence+
			" FROM (SELECT ? AS agent,? AS generation) q", agent, generation).Scan(&placementReady, &oldWorkActive); err != nil {
			return op, err
		}
		if !placementReady {
			return op, ErrRecovery
		}
		if oldWorkActive {
			return op, ErrTargetBusy
		}
	}
	if err := retireExpiredNodeOperations(ctx, tx, agent, now); err != nil {
		return op, err
	}
	if req.Action == nodeops.ContainerReplace || req.Action == nodeops.ContainerCreate || req.Action == nodeops.ContainerRelease || req.Action == nodeops.ComposeSourceCreate || req.Action == nodeops.RegistryPull {
		targetColumn, value := "json_extract(request,'$.target')", req.Target
		if req.Action == nodeops.ComposeSourceCreate {
			// Compose drafts are sealed, but the project is available to history.
			targetColumn = "history_target"
		}
		if req.Action == nodeops.ContainerCreate || req.Action == nodeops.ContainerRelease {
			targetColumn, value = "json_extract(request,'$.name')", req.Name
		}
		if req.Action == nodeops.RegistryPull {
			targetColumn, value = "json_extract(request,'$.image')", req.Image
		}
		var previousActor string
		var previousRequest []byte
		err := tx.QueryRowContext(ctx, `SELECT id,state,actor,request FROM node_operations
 WHERE agent=? AND generation=? AND action=? AND `+targetColumn+`=?
 AND state IN ('queued','running') LIMIT 1`, agent, generation, req.Action, value).
			Scan(&op.ID, &op.State, &previousActor, &previousRequest)
		if err == nil {
			if req.Action == nodeops.ComposeSourceCreate && previousActor == actor {
				previousRequest, err = s.openComposeDraft(op.ID, previousRequest)
				if err != nil {
					return nodeops.Operation{}, ErrTargetBusy
				}
			}
			if req.Action == nodeops.ContainerRelease || previousActor != actor || !bytes.Equal(previousRequest, request) {
				return nodeops.Operation{}, ErrTargetBusy
			}
			op.Agent, op.Generation, op.Action = agent, generation, req.Action
			return op, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nodeops.Operation{}, err
		}
	}
	// Docker output is transient. Retain administrative history separately
	// from output so subsequent operations can purge old logs.
	if _, err := tx.ExecContext(ctx, `UPDATE node_operations SET result=NULL WHERE agent=? AND result IS NOT NULL AND coalesce(completed_ms,created_ms)<?`, agent, now.Add(-time.Hour).UnixMilli()); err != nil {
		return op, err
	}
	// Keep the small terminal receipt while a delivery can still replay it.
	// The foreign keys would otherwise discard both the receipt and its key.
	if _, err := tx.ExecContext(ctx, `DELETE FROM node_operations WHERE created_ms<?
 AND NOT EXISTS (SELECT 1 FROM ci_container_deliveries d JOIN service_credentials c ON c.id=d.credential
                 WHERE d.operation=node_operations.id AND c.revoked=0 AND c.expires_ms>?)
 AND NOT EXISTS (SELECT 1 FROM container_webhook_deliveries d JOIN container_webhooks h ON h.id=d.webhook
                 WHERE d.operation=node_operations.id AND h.revoked=0)`, now.Add(-30*24*time.Hour).UnixMilli(), now.UnixMilli()); err != nil {
		return op, err
	}
	var outstanding int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM node_operations WHERE agent=? AND generation=? AND state IN ('queued','running')`, agent, generation).Scan(&outstanding); err != nil {
		return op, err
	}
	if outstanding >= 8 {
		return op, ErrNodeOperationsBusy
	}
	var key [16]byte
	if _, err := rand.Read(key[:]); err != nil {
		return op, err
	}
	op = nodeops.Operation{ID: hex.EncodeToString(key[:]), Agent: agent, Generation: generation, Action: req.Action, State: nodeops.Queued}
	if req.ComposeSource != nil {
		request, err = s.sealComposeDraft(op.ID, request)
		if err != nil {
			return nodeops.Operation{}, err
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO node_operations(id,agent,generation,actor,action,request,mutation,state,created_ms,expires_ms,history_target)
 VALUES(?,?,?,?,?,?,?,'queued',?,?,?)`, op.ID, op.Agent, op.Generation, actor, op.Action, request, req.Action.Mutates(), now.UnixMilli(), now.Add(nodeOperationQueueTTL).UnixMilli(), nodeOperationHistoryTarget(req))
	if err != nil {
		return nodeops.Operation{}, err
	}
	return op, nil
}

// NextNodeOperation records the claim before sending it. The response is
// never offered a second time, including across control or Agent restarts.
func (s *Store) NextNodeOperation(ctx context.Context, agent string, generation int64, now time.Time) (nodeops.Request, time.Time, error) {
	var req nodeops.Request
	var none time.Time
	var candidate bool
	ms := now.UnixMilli()
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM node_operations WHERE agent=? AND generation=? AND state='queued' AND expires_ms>?)`, agent, generation, ms).Scan(&candidate)
	if err != nil || !candidate {
		if err == nil {
			err = sql.ErrNoRows
		}
		return req, none, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return req, none, err
	}
	defer tx.Rollback()
	req, deadline, err := s.claimNodeOperation(ctx, tx, agent, generation, "", now)
	if err != nil {
		return req, none, err
	}
	return req, deadline, tx.Commit()
}

func (s *Store) claimNodeOperation(ctx context.Context, tx *sql.Tx, agent string, generation int64, expectedID string, now time.Time) (nodeops.Request, time.Time, error) {
	var req nodeops.Request
	var none time.Time
	ms := now.UnixMilli()
	if err := retireExpiredNodeOperations(ctx, tx, agent, now); err != nil {
		return req, none, err
	}
	var id string
	var action nodeops.Action
	var raw []byte
	err := tx.QueryRowContext(ctx, `SELECT q.id,q.action,q.request FROM node_operations q
 WHERE q.agent=? AND q.generation=? AND q.state='queued' AND q.expires_ms>?
 AND `+activeOperationNode+`
 AND NOT `+recoveryAwaitingRelease+`
 AND NOT EXISTS (SELECT 1 FROM node_operations b WHERE b.agent=q.agent AND (b.state='running' OR b.restore_fenced=1) AND b.expires_ms>?)
 AND `+nodeOperationPrincipalAllowed+`
 AND (q.mutation=0 OR (`+restoredNodeMutationAllowed+` AND NOT `+nodeHasActiveRecoveryEvidence+` AND NOT EXISTS (
  SELECT 1 FROM target_slots t JOIN deployments d ON t.deployment_id=d.id WHERE d.agent=q.agent
  ))) ORDER BY q.created_ms,q.rowid LIMIT 1`, agent, generation, ms, agent, generation, ms, ms, ms).Scan(&id, &action, &raw)
	if err != nil {
		return req, none, err
	}
	if expectedID != "" && id != expectedID {
		return req, none, sql.ErrNoRows
	}
	if action.ComposeFileOperation() && action != nodeops.ComposeSourceRead {
		raw, err = s.openComposeDraft(id, raw)
		if err != nil {
			if _, markErr := tx.ExecContext(ctx, `UPDATE node_operations SET state='failed',request='{}',error=? WHERE id=?`,
				"Compose 编辑草稿已失效，请重新读取、预览并提交。", id); markErr != nil {
				return req, none, markErr
			}
			if err := tx.Commit(); err != nil {
				return req, none, err
			}
			return req, none, sql.ErrNoRows
		}
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		return req, none, err
	}
	if err := req.Validate(); err != nil {
		return req, none, err
	}
	req.Operation = id
	deadline := now.Add(nodeOperationStartWindow + req.Action.Timeout())
	expires := deadline
	if action == nodeops.RegistryPull {
		// A lost offer cannot start Docker. RegistryOperation extends this
		// short deadline when the worker requests access, even for public images.
		expires = now.Add(time.Minute)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE node_operations SET state='running',expires_ms=? WHERE id=?`, expires.UnixMilli(), id); err != nil {
		return req, none, err
	}
	return req, deadline, nil
}

func (s *Store) RunningNodeOperation(ctx context.Context, agent string, generation int64, id string, now time.Time) (nodeops.Request, error) {
	return runningNodeOperation(ctx, s.db, agent, generation, id, now)
}

func runningNodeOperation(ctx context.Context, q queryRower, agent string, generation int64, id string, now time.Time) (nodeops.Request, error) {
	var req nodeops.Request
	var raw []byte
	ms := now.UnixMilli()
	err := q.QueryRowContext(ctx, `SELECT q.request FROM node_operations q
 WHERE q.id=? AND q.agent=? AND q.generation=? AND q.state='running' AND q.expires_ms>?
 AND `+activeOperationNode+` AND NOT `+recoveryAwaitingRelease+`
 AND (q.mutation=0 OR (`+restoredNodeMutationAllowed+` AND NOT `+nodeHasActiveRecoveryEvidence+`))
 AND `+nodeOperationPrincipalAllowed, id, agent, generation, ms, agent, generation, ms, ms).Scan(&raw)
	if err != nil {
		return req, err
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		return req, err
	}
	req.Operation = id
	return req, req.Validate()
}

const nodeResultNotConfirmed = "The node did not confirm the result. Refresh Docker resources before retrying."

// CompleteNodeOperation is idempotent for retries on a reconnect. A late
// result can resolve a timeout for the same claim, but not a restore fence
// or a result previously committed by the node.
func (s *Store) CompleteNodeOperation(ctx context.Context, agent string, generation int64, id string, state nodeops.State, failure string, result *nodeops.Result, now time.Time) error {
	if id == "" || len(id) > 128 || now.IsZero() ||
		state != nodeops.Succeeded && state != nodeops.Failed && state != nodeops.Uncertain ||
		state == nodeops.Succeeded && (failure != "" || result == nil) ||
		state != nodeops.Succeeded && (failure == "" || result != nil) ||
		len(failure) > 512 {
		return ErrInvalid
	}
	var encoded []byte
	if result != nil {
		var err error
		encoded, err = json.Marshal(result)
		if err != nil || len(encoded) > nodeops.MaxResultBytes || len(result.Log) > 3*nodeops.MaxLogBytes {
			return ErrInvalid
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := retireExpiredNodeOperations(ctx, tx, agent, now); err != nil {
		return err
	}
	var old nodeops.State
	var action nodeops.Action
	var timedOut bool
	if err := tx.QueryRowContext(ctx, `SELECT state,action,
 mutation=1 AND restore_fenced=0 AND completed_ms IS NULL AND error=?
 FROM node_operations WHERE id=? AND agent=? AND generation=?`,
		nodeResultNotConfirmed, id, agent, generation).Scan(&old, &action, &timedOut); err != nil {
		return err
	}
	var transient []byte
	switch old {
	case nodeops.Running, nodeops.Uncertain:
		if old == nodeops.Uncertain && !timedOut {
			break
		}
		sensitive := action == nodeops.ContainerEditConfig || action == nodeops.ComposeSourceRead
		if result != nil && ((result.EditConfig != nil) != (action == nodeops.ContainerEditConfig) ||
			(result.ComposeSource != nil && len(result.ComposeSource.Files) != 0) != (action == nodeops.ComposeSourceRead)) {
			return ErrInvalid
		}
		if sensitive {
			transient, encoded = encoded, nil
		}
		var historyName string
		historyRequest := "{}"
		if (action == nodeops.ContainerReplace || action == nodeops.ContainerCreate) && result != nil && result.Container != nil {
			historyName = result.Container.Name
			metadata, _ := json.Marshal(map[string]string{"history_image": result.Container.Image})
			historyRequest = string(metadata)
		}
		_, err := tx.ExecContext(ctx, `UPDATE node_operations SET state=?,error=?,result=?,completed_ms=?,
 history_target=coalesce(nullif(?,''),history_target),
 request=CASE WHEN action IN `+privateNodeOperationActions+` THEN ? ELSE request END WHERE id=?`,
			state, failure, encoded, now.UnixMilli(), historyName, historyRequest, id)
		if err != nil {
			return err
		}
		if len(transient) != 0 {
			s.editResults.put(id, transient, now)
		}
	case nodeops.Queued:
		return ErrState
	}
	if err := tx.Commit(); err != nil {
		if len(transient) != 0 {
			s.editResults.remove(id)
		}
		return err
	}
	return nil
}

func (s *Store) GetNodeOperation(ctx context.Context, token, agent, id string, now time.Time) (nodeops.Operation, error) {
	var op nodeops.Operation
	if agent == "" || len(agent) > 128 || len(id) != 32 || strings.TrimSpace(id) != id {
		return op, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return op, err
	}
	defer tx.Rollback()
	actor, err := platformActor(ctx, tx, token)
	if err != nil {
		return op, err
	}
	if err := retireExpiredNodeOperations(ctx, tx, agent, now); err != nil {
		return op, err
	}
	var raw []byte
	err = tx.QueryRowContext(ctx, `SELECT id,agent,generation,action,state,error,CASE WHEN coalesce(completed_ms,created_ms)>? THEN result END
 FROM node_operations WHERE id=? AND agent=? AND (action NOT IN (?,?) OR actor=?)`, now.Add(-time.Hour).UnixMilli(), id, agent, nodeops.ContainerEditConfig, nodeops.ComposeSourceRead, actor).
		Scan(&op.ID, &op.Agent, &op.Generation, &op.Action, &op.State, &op.Error, &raw)
	if err != nil {
		return op, err
	}
	if (op.Action == nodeops.ContainerEditConfig || op.Action == nodeops.ComposeSourceRead) && op.State == nodeops.Succeeded {
		raw = s.editResults.get(id, now)
		if len(raw) == 0 {
			op.State = nodeops.Failed
			op.Error = "编辑配置已过期，请重新读取"
		}
	}
	if len(raw) != 0 {
		if err := json.Unmarshal(raw, &op.Result); err != nil {
			return op, err
		}
	}
	return op, tx.Commit()
}

func retireExpiredNodeOperations(ctx context.Context, tx *sql.Tx, agent string, now time.Time) error {
	_, err := tx.ExecContext(ctx, `UPDATE node_operations
 SET state=CASE WHEN state='running' AND mutation=1 THEN 'uncertain' ELSE 'failed' END,
     request=CASE WHEN action IN `+privateNodeOperationActions+` THEN '{}' ELSE request END,
     error=CASE WHEN state='running' AND mutation=1 THEN ?
                WHEN state='running' THEN 'The node did not return a result.'
                ELSE 'The node did not accept the operation before it expired.' END
 WHERE agent=? AND expires_ms<=? AND state IN ('queued','running')`, nodeResultNotConfirmed, agent, now.UnixMilli())
	return err
}

func nodeMutationInProgress(ctx context.Context, q queryRower, agent string, now time.Time) (bool, error) {
	var running bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS (
  SELECT 1 FROM node_operations WHERE agent=? AND (state='running' OR restore_fenced=1) AND mutation=1 AND expires_ms>?
 )`, agent, now.UnixMilli()).Scan(&running)
	return running, err
}
