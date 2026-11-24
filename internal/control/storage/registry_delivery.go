package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/maxtanforge/moori/internal/core/nodeops"
	"github.com/maxtanforge/moori/internal/core/registry"
	"github.com/maxtanforge/moori/internal/core/release"
)

func (s *Store) ExecutionRegistry(ctx context.Context, key SecretKey, agent string, generation int64, execution, digest string, secure bool) (map[string]registry.Credential, error) {
	if agent == "" || generation <= 0 || execution == "" || len(execution) > 128 || len(digest) != 71 {
		return nil, ErrForbidden
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var id, target, document string
	if err := tx.QueryRowContext(ctx, "SELECT d.id,d.target"+deliveryScope+" AND "+activeOperationNode,
		agent, generation, execution, digest, agent, generation, time.Now().UnixMilli()).Scan(&id, &target); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrForbidden
		}
		return nil, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT document FROM snapshots WHERE deployment_id=?`, id).Scan(&document); err != nil {
		return nil, err
	}
	snapshot, err := release.Verify(ctx, document, digest)
	if err != nil || snapshot.Target != target {
		return nil, ErrForbidden
	}
	images, err := registry.ComposeImages(snapshot.Compose)
	if err != nil {
		return nil, err
	}
	result := make(map[string]registry.Credential)
	for _, image := range images {
		selected, err := selectRegistry(ctx, tx, "", image)
		if err != nil {
			return nil, err
		}
		credential, err := readRegistryCredential(ctx, tx, key, selected, secure)
		if err != nil {
			return nil, err
		}
		if credential != (registry.Credential{}) {
			result[image.Reference] = credential
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}

// AuthorizeRegistryOperation checks the requested scope without retrieving a credential.
func (s *Store) AuthorizeRegistryOperation(ctx context.Context, session, agent, project, target, registryID, value string) error {
	image, err := registry.ParseImage(value)
	if err != nil || project == "" && target != "" {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := platformActor(ctx, tx, session); err != nil {
		return err
	}
	if project != "" {
		if err := verifyRegistryScope(ctx, tx, agent, project, target); err != nil {
			return err
		}
	}
	if _, err := selectRegistry(ctx, tx, registryID, image); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) RegistryOperation(ctx context.Context, key SecretKey, agent string, generation int64, operationID string, secure bool) (registry.Credential, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return registry.Credential{}, err
	}
	defer tx.Rollback()
	request, err := runningNodeOperation(ctx, tx, agent, generation, operationID, time.Now())
	if err != nil {
		return registry.Credential{}, err
	}
	scope, value, allowed := request.RegistryAccess()
	if !allowed {
		return registry.Credential{}, ErrForbidden
	}
	image, err := registry.ParseImage(value)
	if err != nil {
		return registry.Credential{}, ErrForbidden
	}
	if scope.Project != "" {
		if err := verifyRegistryScope(ctx, tx, agent, scope.Project, scope.Target); err != nil {
			return registry.Credential{}, err
		}
	}
	selected, err := selectRegistry(ctx, tx, scope.RegistryID, image)
	if err != nil {
		return registry.Credential{}, err
	}
	cred, err := readRegistryCredential(ctx, tx, key, selected, secure)
	if err != nil {
		return registry.Credential{}, err
	}
	if request.Action == nodeops.RegistryPull {
		// Bound the fence from submission so repeated credential reads cannot prolong it.
		lifetime := nodeOperationQueueTTL + nodeOperationStartWindow + request.Action.Timeout()
		if _, err := tx.ExecContext(ctx, "UPDATE node_operations SET expires_ms=created_ms+? WHERE id=?", lifetime.Milliseconds(), operationID); err != nil {
			return registry.Credential{}, err
		}
	}
	return cred, tx.Commit()
}

type registrySelection struct {
	id, host   string
	ciphertext []byte
}

// Explicit IDs still have to match the image host and repository. Automatic
// selection considers only the longest matching path-component prefix.
func selectRegistry(ctx context.Context, tx *sql.Tx, requested string, image registry.Image) (registrySelection, error) {
	var chosen registrySelection
	rows, err := tx.QueryContext(ctx, `SELECT id,host,repository_prefix,ciphertext FROM registries
		WHERE enabled=1 AND host=? AND (?='' OR id=?)
		AND (repository_prefix='' OR repository_prefix=? OR substr(?,1,length(repository_prefix)+1)=repository_prefix||'/')
		ORDER BY length(repository_prefix) DESC,id LIMIT 2`, image.Host, requested, requested, image.Repository, image.Repository)
	if err != nil {
		return chosen, err
	}
	defer rows.Close()
	longest := -1
	for rows.Next() {
		var candidate registrySelection
		var prefix string
		if err := rows.Scan(&candidate.id, &candidate.host, &prefix, &candidate.ciphertext); err != nil {
			return registrySelection{}, err
		}
		if len(prefix) == longest {
			return registrySelection{}, errors.Join(registry.ErrAmbiguous, registry.ErrDenied)
		}
		if longest < 0 {
			chosen = candidate
			longest = len(prefix)
		}
	}
	if err := rows.Err(); err != nil {
		return registrySelection{}, err
	}
	if requested != "" && chosen.id == "" {
		return registrySelection{}, registry.ErrDenied
	}
	return chosen, nil
}

func readRegistryCredential(ctx context.Context, tx *sql.Tx, key SecretKey, selected registrySelection, secure bool) (registry.Credential, error) {
	var result registry.Credential
	if selected.ciphertext == nil {
		return result, nil
	}
	if !secure {
		return result, registry.ErrSecureTransport
	}
	if err := checkSecretKey(ctx, tx, key); err != nil {
		return result, errors.Join(registry.ErrDenied, err)
	}
	codec, err := secretCipher(key)
	if err != nil {
		return result, errors.Join(registry.ErrDenied, err)
	}
	size := codec.NonceSize()
	if len(selected.ciphertext) < size+codec.Overhead() {
		return result, registry.ErrDenied
	}
	plain, err := codec.Open(nil, selected.ciphertext[:size], selected.ciphertext[size:], registryAAD(selected.id, selected.host))
	if err != nil {
		return result, registry.ErrDenied
	}
	defer clear(plain)
	if err := json.Unmarshal(plain, &result); err != nil || result.ServerAddress != selected.host || result.Username == "" || result.Password == "" {
		return registry.Credential{}, registry.ErrDenied
	}
	return result, nil
}
