ALTER TABLE executions ADD COLUMN document TEXT NOT NULL DEFAULT '';
CREATE INDEX pending_launch ON executions(state, id) WHERE document <> '';
