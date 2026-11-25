DROP INDEX running_target;
CREATE UNIQUE INDEX running_target ON executions(target)
    WHERE state NOT IN ('succeeded','failed','canceled','abandoned');
CREATE TABLE execution_retirements (
    execution_id TEXT PRIMARY KEY NOT NULL REFERENCES executions(id) ON DELETE CASCADE,
    operator TEXT NOT NULL,
    note TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
