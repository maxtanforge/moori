package storage

import (
	"context"
	"crypto/sha256"
)

// LocalExecutorID cannot be registered through the remote enrollment APIs.
// It has no bearer credential and is used only by the in-process executor.
const LocalExecutorID = "@local"

func (s *Store) EnsureLocalExecutor(ctx context.Context) error {
	fingerprint := sha256.Sum256([]byte("moori built-in executor v1"))
	return s.BindAgent(ctx, LocalExecutorID, 1, fingerprint)
}

func (s *Store) HasLocalExecutor(ctx context.Context) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_certificates WHERE agent=?)`, LocalExecutorID).Scan(&exists)
	return exists, err
}
