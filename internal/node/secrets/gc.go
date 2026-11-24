package secrets

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// CollectUnused must run with the Agent stopped and its ledger locked, no
// active ledger executions, and a complete quiescent Docker mount inventory.
// Validate every candidate before removing anything. Never traverse symlinks.
func CollectUnused(root string, mounts []string, before time.Time, apply bool) ([]string, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, err
	}
	if absolute != resolved {
		return nil, errors.New("secret root must not traverse symlinks")
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("secret root is not a directory")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("secret root must be private")
	}
	var sources []string
	for _, source := range mounts {
		if !filepath.IsAbs(source) {
			return nil, errors.New("mount source must be absolute")
		}
		source = filepath.Clean(source)
		if resolved, err := filepath.EvalSymlinks(source); err == nil {
			source = resolved
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		sources = append(sources, source)
	}
	entries, err := os.ReadDir(absolute)
	if err != nil {
		return nil, err
	}
	var candidates []string
	files := map[string][]string{}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "bundle-") {
			continue
		}
		path := filepath.Join(absolute, entry.Name())
		info, err := os.Lstat(path)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("invalid bundle directory")
		}
		if !info.ModTime().Before(before) {
			continue
		}
		mounted := false
		for _, source := range sources {
			if within(path, source) || within(source, path) {
				mounted = true
				break
			}
		}
		if mounted {
			continue
		}
		children, err := os.ReadDir(path)
		if err != nil {
			return nil, err
		}
		for _, child := range children {
			childPath := filepath.Join(path, child.Name())
			info, err := os.Lstat(childPath)
			if err != nil {
				return nil, err
			}
			if !info.Mode().IsRegular() {
				return nil, errors.New("bundle contains a non-regular file")
			}
			files[path] = append(files[path], childPath)
		}
		candidates = append(candidates, path)
	}
	if apply {
		for _, path := range candidates {
			for _, file := range files[path] {
				if err := os.Chmod(file, 0600); err != nil {
					return nil, err
				}
				if err := os.Remove(file); err != nil {
					return nil, err
				}
			}
			if err := os.Remove(path); err != nil {
				return nil, err
			}
		}
	}
	return candidates, nil
}

func within(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
