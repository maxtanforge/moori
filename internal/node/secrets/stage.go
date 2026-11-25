package secrets

import (
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/maxtanforge/moori/internal/core/release"
)

const bundlesLimit = 1024

type fileSource struct {
	File string `json:"file"`
}
type composeMounts struct {
	Services map[string]map[string]json.RawMessage `json:"services"`
	Volumes  map[string]json.RawMessage            `json:"volumes,omitempty"`
	Networks map[string]json.RawMessage            `json:"networks,omitempty"`
	Secrets  map[string]fileSource                 `json:"secrets"`
}

// Stage builds runtime Compose using file sources, never secret values in
// its JSON. The caller retains the files if Docker could have mounted them.
func Stage(root string, budget int64, input release.Verified, values map[string][]byte) (document string, releaseFiles func(), err error) {
	if root == "" || budget <= 0 || len(input.Secrets) == 0 || len(values) != len(input.Secrets) {
		return "", nil, errors.New("invalid secret bundle")
	}
	bindings, err := (release.SecretBindings{Secrets: input.Secrets, Services: input.SecretServices}).Canonical()
	if err != nil {
		return "", nil, err
	}
	var bytes int64
	for name := range bindings.Secrets {
		value := values[name]
		if len(value) == 0 || len(value) > release.MaxSecretValueBytes {
			return "", nil, errors.New("invalid secret bundle value")
		}
		bytes += int64(len(value))
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return "", nil, err
	}
	if strings.ContainsAny(absolute, "$\r\n") {
		return "", nil, errors.New("invalid secret root path")
	}
	if err := os.MkdirAll(absolute, 0700); err != nil {
		return "", nil, err
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return "", nil, err
	}
	if !info.IsDir() || runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		return "", nil, errors.New("secret root must be a private directory")
	}
	if err := checkBudget(absolute, budget, bytes); err != nil {
		return "", nil, err
	}
	var doc composeMounts
	if err := json.Unmarshal([]byte(input.Compose), &doc); err != nil {
		return "", nil, err
	}
	for name, list := range bindings.Services {
		service, ok := doc.Services[name]
		if !ok || service == nil {
			return "", nil, errors.New("secret mount service missing")
		}
		field, err := json.Marshal(list)
		if err != nil {
			return "", nil, err
		}
		service["secrets"] = field
	}
	stage, err := os.MkdirTemp(absolute, "bundle-")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() {
		for name := range bindings.Secrets {
			path := filepath.Join(stage, name)
			// Windows tests need the read-only file attribute cleared before removal.
			_ = os.Chmod(path, 0600)
			_ = os.Remove(path)
		}
		_ = os.Remove(stage)
	}
	defer func() {
		if err != nil {
			cleanup()
		}
	}()
	doc.Secrets = make(map[string]fileSource, len(values))
	for name, value := range values {
		path := filepath.Join(stage, name)
		if err := writeSecret(path, value); err != nil {
			return "", nil, err
		}
		doc.Secrets[name] = fileSource{File: filepath.ToSlash(path)}
	}
	encoded, err := json.Marshal(doc)
	if err != nil {
		return "", nil, err
	}
	return string(encoded), cleanup, nil
}

func writeSecret(path string, value []byte) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = file.Write(value)
	if err == nil {
		err = file.Chmod(0444)
	}
	if err == nil {
		err = file.Sync()
	}
	return errors.Join(err, file.Close())
}

func checkBudget(root string, budget, needed int64) error {
	if needed > budget {
		return errors.New("secret file quota exceeded")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	remaining := budget - needed
	bundles := 0
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "bundle-") {
			continue
		}
		bundles++
		if bundles >= bundlesLimit {
			return errors.New("secret bundle quota exceeded")
		}
		files, err := os.ReadDir(filepath.Join(root, entry.Name()))
		if err != nil {
			return err
		}
		for _, file := range files {
			info, err := file.Info()
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() || info.Size() > remaining {
				return errors.New("secret file quota exceeded or invalid secret storage")
			}
			remaining -= info.Size()
		}
	}
	if budget-remaining >= budget-budget/5 || bundles >= bundlesLimit*4/5 {
		slog.Warn("secret storage approaching quota; run offline gc-secrets", "bytes_after_stage", budget-remaining, "budget", budget, "retained_bundles", bundles)
	}
	return nil
}
