CREATE TABLE host_contacts (
    agent TEXT PRIMARY KEY NOT NULL,
    generation INTEGER NOT NULL,
    at_ms INTEGER NOT NULL
);
CREATE TABLE host_samples (
    agent TEXT PRIMARY KEY NOT NULL,
    generation INTEGER NOT NULL,
    at_ms INTEGER NOT NULL,
    cpu INTEGER,
    memory_bytes INTEGER NOT NULL,
    memory_available_bytes INTEGER NOT NULL,
    disk_bytes INTEGER NOT NULL,
    disk_available_bytes INTEGER NOT NULL
);
