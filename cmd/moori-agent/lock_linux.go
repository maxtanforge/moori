package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

func lockLedger(path string) (*os.File, error) {
	f, err := lockAgentFile(path)
	if err != nil {
		return nil, fmt.Errorf("could not acquire exclusive Agent ledger lock: %w", err)
	}
	return f, nil
}

func lockAgentFile(path string) (*os.File, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	switch {
	case err == nil:
		absolute = canonical
	case errors.Is(err, os.ErrNotExist):
		parent, err := filepath.EvalSymlinks(filepath.Dir(absolute))
		if err != nil {
			return nil, err
		}
		absolute = filepath.Join(parent, filepath.Base(absolute))
	default:
		return nil, err
	}
	file, err := os.OpenFile(absolute+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		return nil, fmt.Errorf("could not acquire exclusive lock for %s: %w", absolute, err)
	}
	// The lock file is kept so a waiting process never locks a different inode.
	return file, nil
}
