package storage

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/maxtanforge/moori/internal/core/deployment"
	"github.com/maxtanforge/moori/internal/platform/sqlite"
)

// Only targets with a placement or a locked execution may have run Docker work.
const markRestoredTargets = `UPDATE targets SET restore_epoch=(SELECT epoch FROM recovery_state WHERE singleton=1)
 WHERE EXISTS (SELECT 1 FROM recovery_state)
 AND (EXISTS (SELECT 1 FROM target_assignments a WHERE a.target=targets.id)
   OR EXISTS (SELECT 1 FROM target_slots s WHERE s.target=targets.id))`

func PrepareRestore(ctx context.Context, backup, destination string) error {
	return sqlite.PrepareRestore(ctx, backup, destination, controlBackupID, controlApplicationID, controlSchemaVersion, func(path string) error {
		s, err := Open(ctx, path)
		if err != nil {
			return err
		}
		err = s.enterRecovery(ctx)
		return errors.Join(err, s.Close())
	})
}

func (s *Store) enterRecovery(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO recovery_state(singleton,epoch) VALUES (1,?)
 ON CONFLICT(singleton) DO UPDATE SET epoch=excluded.epoch,created_at=CURRENT_TIMESTAMP`, "restore-"+rand.Text()); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO project_audit(project,action,actor,subject,detail)
 SELECT g.project,'ci_credential.revoked','system',c.id,g.target
 FROM service_credentials c JOIN ci_credential_scopes g ON g.credential=c.id WHERE c.revoked=0;
 UPDATE service_credentials SET revoked=1 WHERE revoked=0;
 UPDATE agent_credentials SET revoked=1,recovery_pending=1;
 DELETE FROM human_sessions;
 DELETE FROM agent_enrollment_tokens;`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, markRestoredTargets+";"+retireRestoredNodeOperations); err != nil {
		return err
	}
	if err := restoreTransition(ctx, tx, deployment.Canceled, "queued work invalidated by database restore", deployment.Queued); err != nil {
		return err
	}
	if err := restoreTransition(ctx, tx, deployment.Reconciling, "execution outcome requires recovery evidence", deployment.Dispatching, deployment.Executing, deployment.Verifying); err != nil {
		return err
	}
	return tx.Commit()
}

func restoreTransition(ctx context.Context, tx *sql.Tx, to deployment.State, reason string, from ...deployment.State) error {
	// json_each is built into SQLite and lets both statements share one list.
	states, err := json.Marshal(from)
	if err != nil {
		return err
	}
	const filter = " WHERE state IN (SELECT value FROM json_each(?))"
	if _, err := tx.ExecContext(ctx, "INSERT INTO deployment_audit(deployment_id,state,reason) SELECT id,?,? FROM deployments"+filter, to, reason, string(states)); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "UPDATE deployments SET state=?"+filter, to, string(states))
	return err
}

func recoveryEpoch(ctx context.Context, q queryRower) (string, error) {
	var epoch string
	err := q.QueryRowContext(ctx, "SELECT epoch FROM recovery_state WHERE singleton=1").Scan(&epoch)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return epoch, err
}

type RecoveryView struct {
	Epoch              string `json:"epoch,omitempty"`
	Targets            int    `json:"targets"`
	Reports            int    `json:"reports"`
	ObservedExecutions int    `json:"observed_executions"`
	InventoriedAgents  int    `json:"inventoried_agents"`
	ReleasedTargets    int    `json:"released_targets"`
	PendingTargets     int    `json:"pending_targets"`
}

func (s *Store) RecoveryEpoch(ctx context.Context) (string, error) {
	return recoveryEpoch(ctx, s.db)
}

func (s *Store) RecoveryStatus(ctx context.Context) (RecoveryView, error) {
	var v RecoveryView
	err := s.db.QueryRowContext(ctx, `SELECT coalesce(rs.epoch,''),(SELECT count(*) FROM targets),
 (SELECT count(*) FROM recovery_reports e WHERE e.epoch=rs.epoch),
 (SELECT count(*) FROM recovery_inventory i WHERE i.epoch=rs.epoch),
 (SELECT count(*) FROM recovery_inventory_complete c WHERE c.epoch=rs.epoch),
 (SELECT count(*) FROM recovery_target_releases rr JOIN agent_certificates ac ON ac.agent=rr.agent AND ac.generation=rr.generation
  WHERE rr.epoch=rs.epoch),
 (SELECT count(*) FROM targets t WHERE `+needsRecoveryRelease+`)
 FROM (SELECT 1) LEFT JOIN recovery_state rs ON rs.singleton=1`).Scan(&v.Epoch, &v.Targets, &v.Reports, &v.ObservedExecutions, &v.InventoriedAgents, &v.ReleasedTargets, &v.PendingTargets)
	return v, err
}

type RecoveryInventory struct {
	Epoch      string                `json:"epoch"`
	Agent      string                `json:"agent"`
	Generation int64                 `json:"generation"`
	Complete   bool                  `json:"complete"`
	Records    []deployment.Evidence `json:"records"`
	Next       string                `json:"next,omitempty"`
}

func (s *Store) ReadRecoveryInventory(ctx context.Context, agent, after string) (RecoveryInventory, error) {
	if agent == "" || len(agent) > 128 || len(after) > 128 {
		return RecoveryInventory{}, ErrInvalid
	}
	rows, err := s.db.QueryContext(ctx, `SELECT r.epoch,a.generation,c.agent IS NOT NULL,
 coalesce(i.execution_id,''),coalesce(i.target,''),coalesce(i.digest,''),coalesce(i.sequence,0),coalesce(i.state,'')
 FROM recovery_state r JOIN agent_certificates a ON a.agent=?
 LEFT JOIN recovery_inventory_complete c ON c.epoch=r.epoch AND c.agent=a.agent AND c.generation=a.generation
 LEFT JOIN recovery_inventory i ON i.epoch=r.epoch AND i.agent=a.agent AND i.generation=a.generation AND i.execution_id>?
 WHERE r.singleton=1 ORDER BY i.execution_id LIMIT ?`, agent, after, deployment.InventoryPageSize+1)
	if err != nil {
		return RecoveryInventory{}, err
	}
	defer rows.Close()
	result := RecoveryInventory{Agent: agent, Records: make([]deployment.Evidence, 0, deployment.InventoryPageSize)}
	for rows.Next() {
		var item deployment.Evidence
		if err := rows.Scan(&result.Epoch, &result.Generation, &result.Complete, &item.Execution, &item.Target, &item.Digest, &item.Sequence, &item.State); err != nil {
			return RecoveryInventory{}, err
		}
		if item.Execution == "" {
			continue
		}
		if len(result.Records) == deployment.InventoryPageSize {
			result.Next = result.Records[len(result.Records)-1].Execution
			break
		}
		result.Records = append(result.Records, item)
	}
	if err := rows.Err(); err != nil {
		return RecoveryInventory{}, err
	}
	if result.Epoch == "" {
		return RecoveryInventory{}, sql.ErrNoRows
	}
	return result, nil
}

func recordRecoveryReport(ctx context.Context, tx *sql.Tx, epoch string, event Event) error {
	if !agentState(event.State) || len(event.ID) > 256 || len(event.Execution) > 128 || len(event.Agent) > 128 {
		return ErrInvalid
	}
	if err := requireRecoveryAgent(ctx, tx, event.Agent, event.Generation); err != nil {
		return err
	}
	var old Event
	err := tx.QueryRowContext(ctx, `SELECT event_id,execution_id,agent,generation,sequence,state FROM recovery_reports
 WHERE epoch=? AND (event_id=? OR (agent=? AND generation=? AND execution_id=? AND sequence=?))`,
		epoch, event.ID, event.Agent, event.Generation, event.Execution, event.Sequence).Scan(&old.ID, &old.Execution, &old.Agent, &old.Generation, &old.Sequence, &old.State)
	if err == nil {
		if old != event {
			return ErrConflict
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO recovery_reports(epoch,event_id,execution_id,agent,generation,sequence,state)
 VALUES (?,?,?,?,?,?,?)`, epoch, event.ID, event.Execution, event.Agent, event.Generation, event.Sequence, event.State)
	return err
}

func requireRecoveryAgent(ctx context.Context, tx *sql.Tx, agent string, generation int64) error {
	var valid bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM agent_certificates WHERE agent=? AND generation=?)`, agent, generation).Scan(&valid); err != nil {
		return err
	}
	if !valid {
		return ErrForbidden
	}
	return nil
}

func (s *Store) RecordInventory(ctx context.Context, epoch, agent string, generation int64, records []deployment.Evidence, complete bool) error {
	if epoch == "" || len(records) > deployment.InventoryPageSize || len(records) == 0 && !complete {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	current, err := recoveryEpoch(ctx, tx)
	if err != nil {
		return err
	}
	if current != epoch {
		return ErrConflict
	}
	if err := requireRecoveryAgent(ctx, tx, agent, generation); err != nil {
		return err
	}
	for _, r := range records {
		if err := putRecoveryEvidence(ctx, tx, epoch, agent, generation, r); err != nil {
			return err
		}
	}
	if complete {
		if _, err := tx.ExecContext(ctx, "INSERT INTO recovery_inventory_complete VALUES (?,?,?) ON CONFLICT DO NOTHING", epoch, agent, generation); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func putRecoveryEvidence(ctx context.Context, tx *sql.Tx, epoch, agent string, generation int64, r deployment.Evidence) error {
	digest, ok := strings.CutPrefix(r.Digest, "sha256:")
	if !ok || len(digest) != 64 {
		return ErrInvalid
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return ErrInvalid
	}
	if r.Execution == "" || len(r.Execution) > 128 || r.Target == "" || len(r.Target) > 128 ||
		!(r.State == deployment.Dispatching && r.Sequence == 0 || agentState(r.State) && r.Sequence > 0) {
		return ErrInvalid
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO recovery_inventory VALUES (?,?,?,?,?,?,?,?)
 ON CONFLICT(epoch,agent,generation,execution_id) DO UPDATE SET
 sequence=max(recovery_inventory.sequence,excluded.sequence),
 state=CASE WHEN excluded.sequence>recovery_inventory.sequence THEN excluded.state ELSE recovery_inventory.state END
 WHERE recovery_inventory.target=excluded.target AND recovery_inventory.digest=excluded.digest AND
 (excluded.sequence<>recovery_inventory.sequence OR excluded.state=recovery_inventory.state)`,
		epoch, agent, generation, r.Execution, r.Target, r.Digest, r.Sequence, r.State)
	return expectOne(res, err, ErrConflict)
}
