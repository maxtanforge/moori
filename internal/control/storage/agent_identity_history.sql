-- Keep generation fences and consumed browser enrollment requests across deletion.
CREATE TABLE agent_identity_history (
    agent TEXT NOT NULL,
    generation INTEGER NOT NULL CHECK(generation>0),
    request_hash BLOB UNIQUE,
    retired_fingerprint BLOB UNIQUE,
    PRIMARY KEY(agent, generation)
);
