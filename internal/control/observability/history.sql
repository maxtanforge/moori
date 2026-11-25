CREATE TABLE metric_history_series (
    id INTEGER PRIMARY KEY,
    agent TEXT NOT NULL,
    generation INTEGER NOT NULL,
    container TEXT NOT NULL,
    name TEXT NOT NULL,
    first_sample_ms INTEGER NOT NULL,
    last_sample_ms INTEGER NOT NULL,
    counters BLOB NOT NULL,
    UNIQUE (agent,generation,container)
);
CREATE INDEX metric_history_series_expiration ON metric_history_series(last_sample_ms);

CREATE TABLE metric_history_points (
    series_id INTEGER NOT NULL REFERENCES metric_history_series(id) ON DELETE CASCADE,
    slot_ms INTEGER NOT NULL,
    at_ms INTEGER NOT NULL,
    cpu_percent REAL,
    memory_bytes INTEGER,
    memory_limit_bytes INTEGER,
    disk_used_bytes INTEGER,
    disk_total_bytes INTEGER,
    rx_bytes_sec REAL,
    tx_bytes_sec REAL,
    read_bytes_sec REAL,
    write_bytes_sec REAL,
    PRIMARY KEY (series_id,slot_ms)
);
CREATE INDEX metric_history_points_expiration ON metric_history_points(at_ms);
