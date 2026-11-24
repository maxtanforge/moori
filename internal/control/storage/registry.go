package storage

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"strings"

	"github.com/maxtanforge/moori/internal/core/registry"
)

type RegistryInput struct {
	Name             string `json:"name"`
	Host             string `json:"host"`
	RepositoryPrefix string `json:"repository_prefix"`
	Username         string `json:"username"`
	Password         string `json:"password"`
}

type RegistryBinding struct {
	Project string `json:"project"`
	Agent   string `json:"agent"`
	Target  string `json:"target"`
}

type Registry struct {
	ID               string            `json:"id"`
	Name             string            `json:"name"`
	Host             string            `json:"host"`
	RepositoryPrefix string            `json:"repository_prefix"`
	Enabled          bool              `json:"enabled"`
	HasCredential    bool              `json:"has_credential"`
	Bindings         []RegistryBinding `json:"bindings"`
}

func (s *Store) ListHumanRegistries(ctx context.Context, session, after string) (CatalogPage[Registry], error) {
	page := CatalogPage[Registry]{Items: []Registry{}}
	if len(after) > 128 {
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
	rows, err := tx.QueryContext(ctx, `SELECT id,name,host,repository_prefix,enabled,ciphertext IS NOT NULL
      FROM registries WHERE id>? ORDER BY id LIMIT 51`, after)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		if len(page.Items) == 50 {
			page.Next = page.Items[49].ID
			break
		}
		item := Registry{Bindings: []RegistryBinding{}}
		if err := rows.Scan(&item.ID, &item.Name, &item.Host, &item.RepositoryPrefix, &item.Enabled, &item.HasCredential); err != nil {
			return page, err
		}
		page.Items = append(page.Items, item)
	}
	if err := rows.Err(); err != nil {
		return page, err
	}
	if err := rows.Close(); err != nil {
		return page, err
	}
	// One pass for every listed registry instead of a query per registry.
	if len(page.Items) > 0 {
		ids := make([]string, len(page.Items))
		for i := range page.Items {
			ids[i] = page.Items[i].ID
		}
		args := make([]any, len(ids))
		for i, id := range ids {
			args[i] = id
		}
		rows, err := tx.QueryContext(ctx, `SELECT registry_id,project,agent,target FROM registry_bindings
 WHERE registry_id IN (`+strings.Repeat("?,", len(ids)-1)+`?) ORDER BY project,agent,target`,
			args...)
		if err != nil {
			return page, err
		}
		bindings := make(map[string][]RegistryBinding, len(ids))
		for rows.Next() {
			var id string
			var binding RegistryBinding
			if err := rows.Scan(&id, &binding.Project, &binding.Agent, &binding.Target); err != nil {
				rows.Close()
				return page, err
			}
			bindings[id] = append(bindings[id], binding)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return page, err
		}
		if err := rows.Close(); err != nil {
			return page, err
		}
		for i := range page.Items {
			if bound := bindings[page.Items[i].ID]; bound != nil {
				page.Items[i].Bindings = bound
			}
		}
	}
	return page, tx.Commit()
}

func (s *Store) CreateHumanRegistry(ctx context.Context, session string, key SecretKey, in RegistryInput) (Registry, error) {
	host, err := registry.NormalizeHost(in.Host)
	if err != nil {
		return Registry{}, ErrInvalid
	}
	prefix, err := registry.NormalizeRepositoryPrefix(in.RepositoryPrefix)
	if err != nil {
		return Registry{}, ErrInvalid
	}
	name := strings.TrimSpace(in.Name)
	if name == "" || len(name) > 128 || strings.ContainsAny(name, "\x00\r\n") {
		return Registry{}, ErrInvalid
	}
	item := Registry{ID: "reg-" + rand.Text(), Name: name, Host: host, RepositoryPrefix: prefix, Enabled: true,
		HasCredential: in.Username != "" || in.Password != "", Bindings: []RegistryBinding{}}
	err = s.mutateRegistry(ctx, session, func(tx *sql.Tx, actor string) error {
		var encrypted []byte
		if item.HasCredential {
			var err error
			encrypted, err = encryptRegistryCredential(ctx, tx, key, item.ID, item.Host, in.Username, in.Password)
			if err != nil {
				return err
			}
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO registries(id,name,host,repository_prefix,ciphertext)
          VALUES(?,?,?,?,?) ON CONFLICT(name) DO NOTHING`, item.ID, item.Name, item.Host, item.RepositoryPrefix, encrypted)
		if err := expectOne(res, err, ErrConflict); err != nil {
			return err
		}
		return addProjectAudit(ctx, tx, "", "registry.created", actor, item.ID, item.Host)
	})
	return item, err
}

// UpdateHumanRegistry renames a registry and adjusts its address or
// repository prefix. A stored credential is bound to the registered host as
// AEAD associated data, so the host is locked while a credential exists; a
// different address requires a new registry.
func (s *Store) UpdateHumanRegistry(ctx context.Context, session, id string, in RegistryInput) error {
	host, err := registry.NormalizeHost(in.Host)
	if err != nil {
		return ErrInvalid
	}
	prefix, err := registry.NormalizeRepositoryPrefix(in.RepositoryPrefix)
	if err != nil {
		return ErrInvalid
	}
	name := strings.TrimSpace(in.Name)
	if name == "" || len(name) > 128 || strings.ContainsAny(name, "\x00\r\n") {
		return ErrInvalid
	}
	return s.mutateRegistry(ctx, session, func(tx *sql.Tx, actor string) error {
		var currentName, currentHost, currentPrefix string
		var hasCredential bool
		if err := tx.QueryRowContext(ctx, `SELECT name,host,repository_prefix,ciphertext IS NOT NULL FROM registries WHERE id=?`,
			id).Scan(&currentName, &currentHost, &currentPrefix, &hasCredential); err != nil {
			return err
		}
		if host != currentHost && hasCredential {
			return ErrRegistryHostBound
		}
		if name != currentName {
			var duplicate bool
			if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM registries WHERE name=? AND id<>?)`, name, id).Scan(&duplicate); err != nil {
				return err
			}
			if duplicate {
				return ErrConflict
			}
		}
		res, err := tx.ExecContext(ctx, `UPDATE registries SET name=?,host=?,repository_prefix=? WHERE id=?`, name, host, prefix, id)
		if err := expectOne(res, err, ErrConflict); err != nil {
			return err
		}
		return addProjectAudit(ctx, tx, "", "registry.updated", actor, id, host)
	})
}

func (s *Store) RotateHumanRegistryCredential(ctx context.Context, session string, key SecretKey, id, username, password string) error {
	return s.mutateRegistry(ctx, session, func(tx *sql.Tx, actor string) error {
		var host string
		if err := tx.QueryRowContext(ctx, `SELECT host FROM registries WHERE id=?`, id).Scan(&host); err != nil {
			return err
		}
		encrypted, err := encryptRegistryCredential(ctx, tx, key, id, host, username, password)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE registries SET ciphertext=? WHERE id=?`, encrypted, id); err != nil {
			return err
		}
		return addProjectAudit(ctx, tx, "", "registry.credential_rotated", actor, id, host)
	})
}

func (s *Store) SetHumanRegistryEnabled(ctx context.Context, session, id string, enabled bool) error {
	return s.mutateRegistry(ctx, session, func(tx *sql.Tx, actor string) error {
		var current bool
		if err := tx.QueryRowContext(ctx, `SELECT enabled FROM registries WHERE id=?`, id).Scan(&current); err != nil {
			return err
		}
		if current == enabled {
			return nil
		}
		if _, err := tx.ExecContext(ctx, `UPDATE registries SET enabled=? WHERE id=?`, enabled, id); err != nil {
			return err
		}
		action := "registry.disabled"
		if enabled {
			action = "registry.enabled"
		}
		return addProjectAudit(ctx, tx, "", action, actor, id, "")
	})
}

func (s *Store) SetHumanRegistryBinding(ctx context.Context, session, id string, binding RegistryBinding, enabled bool) error {
	if !enrollmentAgentID.MatchString(binding.Project) || binding.Agent != LocalExecutorID && !enrollmentAgentID.MatchString(binding.Agent) ||
		binding.Target != "" && !enrollmentAgentID.MatchString(binding.Target) {
		return ErrInvalid
	}
	return s.mutateRegistry(ctx, session, func(tx *sql.Tx, actor string) error {
		var host, prefix string
		if err := tx.QueryRowContext(ctx, "SELECT host,repository_prefix FROM registries WHERE id=?", id).Scan(&host, &prefix); err != nil {
			return err
		}
		var res sql.Result
		var err error
		action := "registry.binding_removed"
		if enabled {
			if err := verifyRegistryScope(ctx, tx, binding.Agent, binding.Project, binding.Target); err != nil {
				return err
			}
			if err := verifyRegistryBindingUnique(ctx, tx, id, host, prefix, binding); err != nil {
				return err
			}
			res, err = tx.ExecContext(ctx, `INSERT INTO registry_bindings(registry_id,project,agent,target) VALUES(?,?,?,?) ON CONFLICT DO NOTHING`, id, binding.Project, binding.Agent, binding.Target)
			action = "registry.bound"
		} else {
			res, err = tx.ExecContext(ctx, `DELETE FROM registry_bindings WHERE registry_id=? AND project=? AND agent=? AND target=?`, id, binding.Project, binding.Agent, binding.Target)
		}
		if err != nil {
			return err
		}
		affected, err := res.RowsAffected()
		if err != nil || affected == 0 {
			return err
		}
		detail, _ := json.Marshal(binding)
		return addProjectAudit(ctx, tx, binding.Project, action, actor, id, string(detail))
	})
}

func (s *Store) mutateRegistry(ctx context.Context, session string, fn func(*sql.Tx, string) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	actor, err := platformActor(ctx, tx, session)
	if err != nil {
		return err
	}
	if err := requireRecoveryComplete(ctx, tx); err != nil {
		return err
	}
	if err := fn(tx, actor); err != nil {
		return err
	}
	return tx.Commit()
}

func verifyRegistryScope(ctx context.Context, q queryRower, agent, project, target string) error {
	var exists bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_certificates a WHERE a.agent=?)
      AND EXISTS(SELECT 1 FROM targets t WHERE t.project=? AND (?='' OR (t.id=? AND EXISTS (
        SELECT 1 FROM target_assignments ta WHERE ta.target=t.id AND ta.agent=?))))`, agent, project, target, target, agent).Scan(&exists)
	if err != nil {
		return err
	}
	if !exists {
		return registry.ErrDenied
	}
	return nil
}

func verifyRegistryBindingUnique(ctx context.Context, tx *sql.Tx, id, host, prefix string, binding RegistryBinding) error {
	rows, err := tx.QueryContext(ctx, `SELECT r.repository_prefix
	  FROM registries r JOIN registry_bindings b ON r.id=b.registry_id
	  WHERE r.id<>? AND r.host=? AND b.project=? AND b.agent=?
	  AND (b.target='' OR ?='' OR b.target=?)`, id, host, binding.Project, binding.Agent, binding.Target, binding.Target)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var other string
		if err := rows.Scan(&other); err != nil {
			return err
		}
		if registry.InRepository(prefix, other) || registry.InRepository(other, prefix) {
			return ErrConflict
		}
	}
	return rows.Err()
}
func registryAAD(id, host string) []byte {
	encoded, _ := json.Marshal([3]string{"moori-registry-credential-v1", id, host})
	return encoded
}

func encryptRegistryCredential(ctx context.Context, tx *sql.Tx, key SecretKey, id, host, username, password string) ([]byte, error) {
	if strings.TrimSpace(username) == "" || password == "" || len(username) > 256 || len(password) > 4096 ||
		strings.ContainsAny(username, "\x00\r\n") || strings.ContainsRune(password, '\x00') {
		return nil, ErrInvalid
	}
	if err := checkSecretKey(ctx, tx, key); err != nil {
		return nil, err
	}
	codec, err := secretCipher(key)
	if err != nil {
		return nil, err
	}
	fingerprint := sha256.Sum256(key[:])
	if _, err := tx.ExecContext(ctx, `INSERT INTO secret_vault VALUES(1,?) ON CONFLICT DO NOTHING`, fingerprint[:]); err != nil {
		return nil, err
	}
	plain, err := json.Marshal(registry.Credential{Username: username, Password: password, ServerAddress: host})
	if err != nil {
		return nil, err
	}
	defer clear(plain)
	nonce := make([]byte, codec.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return codec.Seal(nonce, nonce, plain, registryAAD(id, host)), nil
}
