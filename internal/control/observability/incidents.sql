CREATE TABLE active_incidents (
    agent TEXT NOT NULL,
    generation INTEGER NOT NULL,
    code TEXT NOT NULL,
    opened_ms INTEGER NOT NULL,
    PRIMARY KEY (agent, code)
);
