CREATE TABLE executions (
    id TEXT PRIMARY KEY NOT NULL,
    target TEXT NOT NULL,
    digest TEXT NOT NULL,
    expires_ms INTEGER NOT NULL,
    state TEXT NOT NULL,
    sequence INTEGER NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX running_target ON executions(target)
    WHERE state NOT IN ('succeeded','failed','canceled');
CREATE TABLE execution_events (
    ordinal INTEGER PRIMARY KEY,
    id TEXT UNIQUE NOT NULL,
    execution TEXT NOT NULL REFERENCES executions(id) ON DELETE CASCADE,
    sequence INTEGER NOT NULL,
    state TEXT NOT NULL,
    acknowledged INTEGER NOT NULL DEFAULT 0,
    UNIQUE (execution, sequence)
);
CREATE INDEX pending_events ON execution_events(ordinal) WHERE acknowledged = 0;
CREATE TABLE retention (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    cutoff_ms INTEGER NOT NULL
);
INSERT INTO retention VALUES (1, 0);

CREATE TABLE node_operation_receipts (
    id TEXT PRIMARY KEY NOT NULL,
    state TEXT NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT '',
    result BLOB
);
