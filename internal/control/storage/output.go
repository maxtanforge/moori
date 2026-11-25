package storage

import "context"

func (s *Store) CanAttachOutput(ctx context.Context, execution, agent string, generation int64) (bool, error) {
	var allowed bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM deployments d
 WHERE execution_id=? AND agent=? AND generation=?
 AND d.dispatch_epoch=coalesce((SELECT epoch FROM recovery_state WHERE singleton=1),''))`, execution, agent, generation).Scan(&allowed)
	return allowed, err
}
