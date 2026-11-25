package storage

import (
	"context"
	"database/sql"
	"errors"

	"github.com/maxtanforge/moori/internal/core/release"
)

const deliveryScope = ` FROM deployments d JOIN agent_certificates a
 ON a.agent=d.agent AND a.generation=d.generation WHERE d.agent=? AND d.generation=?
 AND d.execution_id=? AND d.digest=? AND d.state IN ('dispatching','reconciling')
 AND NOT EXISTS (SELECT 1 FROM deployment_cancellations c WHERE c.deployment_id=d.id)
 AND d.dispatch_epoch=coalesce((SELECT epoch FROM recovery_state WHERE singleton=1),'')
 AND ` + canDispatchTarget

func (s *Store) ExecutionSecrets(ctx context.Context, key SecretKey, agent string, generation int64, execution, digest string) (map[string][]byte, error) {
	if agent == "" || generation <= 0 || execution == "" || len(execution) > 128 || len(digest) != 71 {
		return nil, ErrForbidden
	}
	args := []any{agent, generation, execution, digest}
	var id string
	if err := s.db.QueryRowContext(ctx, "SELECT d.id"+deliveryScope, args...).Scan(&id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrForbidden
		}
		return nil, err
	}
	document, err := s.ExecutionSnapshot(ctx, id)
	if err != nil {
		return nil, err
	}
	snapshot, err := release.Verify(ctx, document, digest)
	if err != nil {
		return nil, err
	}
	if len(snapshot.Secrets) == 0 {
		return nil, ErrSecretUnavailable
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var project, target string
	if err := tx.QueryRowContext(ctx, "SELECT d.project,d.target"+deliveryScope, args...).Scan(&project, &target); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrForbidden
		}
		return nil, err
	}
	if target != snapshot.Target {
		return nil, ErrForbidden
	}
	if err := checkSecretKey(ctx, tx, key); err != nil {
		return nil, err
	}
	codec, err := secretCipher(key)
	if err != nil {
		return nil, err
	}
	bundle := make(map[string][]byte, len(snapshot.Secrets))
	var finished bool
	defer func() {
		if !finished {
			for _, value := range bundle {
				clear(value)
			}
		}
	}()
	for name, version := range snapshot.Secrets {
		var encrypted []byte
		err := tx.QueryRowContext(ctx, `SELECT ciphertext FROM target_secrets
 WHERE project=? AND target=? AND name=? AND version=? AND revoked=0`, project, target, name, version).Scan(&encrypted)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrSecretUnavailable
		}
		if err != nil {
			return nil, err
		}
		size := codec.NonceSize()
		if len(encrypted) < size+codec.Overhead() || len(encrypted) > size+codec.Overhead()+release.MaxSecretValueBytes {
			return nil, ErrSecretKey
		}
		plaintext, err := codec.Open(nil, encrypted[:size], encrypted[size:], secretAAD(project, target, name, version))
		if err != nil {
			return nil, ErrSecretKey
		}
		bundle[name] = plaintext
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	finished = true
	return bundle, nil
}
