CREATE TABLE agent_inventories (
    agent TEXT PRIMARY KEY NOT NULL,
    generation INTEGER NOT NULL,
    at_ms INTEGER NOT NULL,
    overview TEXT NOT NULL
);
