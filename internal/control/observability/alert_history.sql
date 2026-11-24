CREATE TABLE alert_generations (
    agent TEXT PRIMARY KEY NOT NULL,
    generation INTEGER NOT NULL
);
INSERT INTO alert_generations(agent,generation)
SELECT agent, max(generation) FROM (
    SELECT agent,generation FROM host_contacts
    UNION ALL SELECT agent,generation FROM host_samples
    UNION ALL SELECT agent,generation FROM active_incidents
) GROUP BY agent;

CREATE TABLE alert_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    agent TEXT NOT NULL,
    generation INTEGER NOT NULL,
    code TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('fired','recovered')),
    at_ms INTEGER NOT NULL
);
CREATE INDEX alert_events_history ON alert_events (agent, generation, id DESC);
INSERT INTO alert_events(agent,generation,code,state,at_ms)
SELECT agent,generation,code,'fired',opened_ms
FROM active_incidents ORDER BY opened_ms,agent,code;

DELETE FROM active_incidents
WHERE generation < (SELECT generation FROM alert_generations WHERE agent=active_incidents.agent);

CREATE TABLE alert_deliveries (
    event_id INTEGER PRIMARY KEY REFERENCES alert_events(id) ON DELETE CASCADE,
    attempts INTEGER NOT NULL DEFAULT 0,
    available_ms INTEGER NOT NULL,
    expires_ms INTEGER NOT NULL
);
CREATE INDEX alert_deliveries_available ON alert_deliveries (available_ms, event_id);
CREATE INDEX alert_deliveries_expiry ON alert_deliveries (expires_ms);

CREATE TABLE alert_breach_windows (
    agent TEXT NOT NULL,
    generation INTEGER NOT NULL,
    code TEXT NOT NULL CHECK (code IN ('cpu_high','memory_high')),
    percent INTEGER NOT NULL,
    started_ms INTEGER NOT NULL,
    last_ms INTEGER NOT NULL,
    gap_ms INTEGER NOT NULL,
    PRIMARY KEY (agent, code)
);

CREATE TRIGGER alert_breach_window_sample AFTER UPDATE ON host_samples
BEGIN
    DELETE FROM alert_breach_windows
    WHERE agent=NEW.agent AND (
        generation<>NEW.generation OR NEW.at_ms-last_ms>gap_ms
        OR (code='cpu_high' AND (NEW.cpu IS NULL OR NEW.cpu<percent*100))
        OR (code='memory_high' AND NEW.memory_bytes-NEW.memory_available_bytes
            < NEW.memory_bytes/100*percent+(NEW.memory_bytes%100*percent+99)/100)
    );
    UPDATE alert_breach_windows SET last_ms=NEW.at_ms WHERE agent=NEW.agent;
END;
