-- Secondary indexes added after the initial schema.
-- deployments_target_position lets per-environment history pages walk a
-- target's deployments in insertion order (rowid) without sorting the whole
-- set, and node_operations_generation_time serves container operation history
-- pages that previously scanned every stored operation for the node.
CREATE INDEX deployments_target_position ON deployments(target);
CREATE INDEX node_operations_generation_time ON node_operations(agent,generation,created_ms,id);
