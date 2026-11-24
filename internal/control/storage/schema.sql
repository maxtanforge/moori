CREATE TABLE targets (
    id TEXT PRIMARY KEY NOT NULL,
    project TEXT NOT NULL,
    last_success TEXT NOT NULL DEFAULT '',
    restore_epoch TEXT NOT NULL DEFAULT '',
    UNIQUE (project, id)
);

CREATE TABLE deployments (
    id TEXT PRIMARY KEY NOT NULL,
    project TEXT NOT NULL,
    caller TEXT NOT NULL,
    request_key TEXT NOT NULL,
    target TEXT NOT NULL,
    release_id TEXT NOT NULL,
    digest TEXT NOT NULL,
    state TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_reason TEXT NOT NULL DEFAULT '',
    execution_id TEXT UNIQUE,
    agent TEXT,
    generation INTEGER,
    sequence INTEGER NOT NULL DEFAULT 0,
    expires_ms INTEGER,
    agent_accepted INTEGER NOT NULL DEFAULT 0,
    credential_id TEXT REFERENCES service_credentials(id),
    dispatch_epoch TEXT NOT NULL DEFAULT '',
    rollback_of TEXT REFERENCES deployments(id),
    rollback_reason TEXT NOT NULL DEFAULT '',
    human_submitter TEXT REFERENCES human_users(id),
    UNIQUE (project, caller, request_key),
    UNIQUE (target, id),
    FOREIGN KEY (project, target) REFERENCES targets(project, id)
);

CREATE TABLE target_slots (
    target TEXT PRIMARY KEY NOT NULL,
    deployment_id TEXT NOT NULL UNIQUE,
    FOREIGN KEY (target, deployment_id) REFERENCES deployments(target, id)
);

CREATE TABLE receipts (
    id TEXT PRIMARY KEY NOT NULL,
    execution_id TEXT NOT NULL REFERENCES deployments(execution_id),
    sequence INTEGER NOT NULL,
    state TEXT NOT NULL,
    UNIQUE (execution_id, sequence)
);

CREATE TABLE deployment_audit (
    id INTEGER PRIMARY KEY,
    deployment_id TEXT NOT NULL REFERENCES deployments(id),
    state TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    reason TEXT NOT NULL DEFAULT '');

CREATE TABLE outbox (
    event_id TEXT PRIMARY KEY NOT NULL REFERENCES receipts(id),
    deployment_id TEXT NOT NULL REFERENCES deployments(id),
    state TEXT NOT NULL
);

CREATE TABLE releases (
    project TEXT NOT NULL,
    id TEXT NOT NULL,
    digest TEXT NOT NULL,
    compose TEXT NOT NULL,
    PRIMARY KEY (project, id)
);

CREATE TABLE snapshots (
    deployment_id TEXT PRIMARY KEY NOT NULL REFERENCES deployments(id),
    document TEXT NOT NULL
);

CREATE TABLE target_assignments (
    target TEXT PRIMARY KEY NOT NULL REFERENCES targets(id),
    agent TEXT NOT NULL,
    generation INTEGER NOT NULL CHECK (generation > 0)
);

CREATE TABLE agent_certificates (
    agent TEXT PRIMARY KEY NOT NULL,
    generation INTEGER NOT NULL CHECK (generation > 0),
    fingerprint BLOB UNIQUE NOT NULL CHECK (length(fingerprint) = 32)
);

CREATE TABLE service_credentials (
    id TEXT PRIMARY KEY NOT NULL,
    project TEXT NOT NULL,
    target TEXT NOT NULL,
    token_hash BLOB UNIQUE NOT NULL CHECK (length(token_hash) = 32),
    expires_ms INTEGER NOT NULL,
    revoked INTEGER NOT NULL DEFAULT 0,
    can_publish INTEGER NOT NULL DEFAULT 0 CHECK (can_publish IN (0,1)),
    FOREIGN KEY (project, target) REFERENCES targets(project, id)
);

CREATE TABLE human_users (
    id TEXT PRIMARY KEY NOT NULL,
    username TEXT UNIQUE NOT NULL,
    salt BLOB NOT NULL CHECK (length(salt) = 16),
    password_hash BLOB NOT NULL CHECK (length(password_hash) = 32),
    password_failures INTEGER NOT NULL DEFAULT 0 CHECK(password_failures >= 0),
    password_locked_until_ms INTEGER NOT NULL DEFAULT 0 CHECK(password_locked_until_ms >= 0));

CREATE TABLE human_sessions (
    token_hash BLOB PRIMARY KEY NOT NULL CHECK (length(token_hash) = 32),
    user_id TEXT NOT NULL REFERENCES human_users(id) ON DELETE CASCADE,
    expires_ms INTEGER NOT NULL
);

CREATE TABLE recovery_state (
    singleton INTEGER PRIMARY KEY CHECK (singleton=1),
    epoch TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE recovery_reports (
    epoch TEXT NOT NULL,
    event_id TEXT NOT NULL,
    execution_id TEXT NOT NULL,
    agent TEXT NOT NULL,
    generation INTEGER NOT NULL,
    sequence INTEGER NOT NULL,
    state TEXT NOT NULL,
    received_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (epoch, event_id),
    UNIQUE (epoch, agent, generation, execution_id, sequence)
);

CREATE TABLE recovery_inventory (
    epoch TEXT NOT NULL,
    agent TEXT NOT NULL,
    generation INTEGER NOT NULL,
    execution_id TEXT NOT NULL,
    target TEXT NOT NULL,
    digest TEXT NOT NULL,
    sequence INTEGER NOT NULL,
    state TEXT NOT NULL,
    PRIMARY KEY (epoch, agent, generation, execution_id)
);

CREATE TABLE recovery_inventory_complete (
    epoch TEXT NOT NULL,
    agent TEXT NOT NULL,
    generation INTEGER NOT NULL,
    PRIMARY KEY (epoch, agent, generation)
);

CREATE TABLE recovery_target_releases (
    epoch TEXT NOT NULL,
    target TEXT NOT NULL REFERENCES targets(id),
    agent TEXT NOT NULL,
    generation INTEGER NOT NULL,
    operator TEXT NOT NULL,
    note TEXT NOT NULL,
    released_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (epoch, target)
);

CREATE TABLE deployment_cancellations (
    deployment_id TEXT PRIMARY KEY NOT NULL REFERENCES deployments(id),
    operator TEXT NOT NULL,
    note TEXT NOT NULL,
    requested_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    acknowledged INTEGER NOT NULL DEFAULT 0 CHECK (acknowledged IN (0,1))
);

CREATE TABLE target_releases (
    project TEXT NOT NULL,
    target TEXT NOT NULL,
    id TEXT NOT NULL,
    digest TEXT NOT NULL,
    compose TEXT NOT NULL,
    PRIMARY KEY (project, target, id),
    FOREIGN KEY (project, target) REFERENCES targets(project, id)
);

CREATE TABLE target_templates (
    project TEXT NOT NULL,
    target TEXT NOT NULL,
    id TEXT NOT NULL,
    compose TEXT NOT NULL,
    allowed_images TEXT NOT NULL,
    PRIMARY KEY (project, target, id),
    FOREIGN KEY (project, target) REFERENCES targets(project, id)
);

CREATE TABLE secret_vault (
  singleton INTEGER PRIMARY KEY CHECK (singleton=1),
    fingerprint BLOB NOT NULL CHECK (length(fingerprint)=32)
);

CREATE TABLE target_secrets (
  project TEXT NOT NULL,
    target TEXT NOT NULL,
    name TEXT NOT NULL,
    version TEXT NOT NULL,
    ciphertext BLOB NOT NULL,
    revoked INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY(project,target,name,version),
    FOREIGN KEY(project,target) REFERENCES targets(project,id)
);

CREATE TABLE target_secret_configs (
  project TEXT NOT NULL,
    target TEXT NOT NULL,
    version TEXT NOT NULL,
    bindings TEXT NOT NULL,
    PRIMARY KEY(project,target,version),
    FOREIGN KEY(project,target) REFERENCES targets(project,id)
);

CREATE TABLE active_target_secrets (
  project TEXT NOT NULL,
    target TEXT NOT NULL,
    version TEXT NOT NULL,
    PRIMARY KEY(project,target),
    FOREIGN KEY(project,target,version) REFERENCES target_secret_configs(project,target,version)
);

CREATE TABLE managed_agent_identities (
    agent TEXT PRIMARY KEY REFERENCES agent_certificates(agent),
    generation INTEGER NOT NULL,
    lineage TEXT NOT NULL UNIQUE
);

CREATE TABLE agent_enrollment_tokens (
    token_hash BLOB PRIMARY KEY,
    agent TEXT NOT NULL UNIQUE,
    expires_ms INTEGER NOT NULL,
    csr_hash BLOB,
    certificate BLOB
);

CREATE TABLE agent_certificate_renewals (
    old_fingerprint BLOB PRIMARY KEY,
    expires_ms INTEGER NOT NULL,
    csr_hash BLOB NOT NULL,
    certificate BLOB NOT NULL
);

CREATE TABLE project_members (
    project TEXT NOT NULL,
    user_id TEXT NOT NULL REFERENCES human_users(id),
    role TEXT NOT NULL CHECK (role IN ('viewer','operator','project_admin')),
    PRIMARY KEY(project,user_id)
);

CREATE TABLE platform_admins (
    user_id TEXT PRIMARY KEY NOT NULL REFERENCES human_users(id)
);

CREATE TABLE project_audit (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    project TEXT NOT NULL,
    action TEXT NOT NULL,
    actor TEXT NOT NULL,
    subject TEXT NOT NULL,
    detail TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE agent_credentials (
 agent TEXT PRIMARY KEY,
    generation INTEGER NOT NULL CHECK(generation>0),
    token_hash BLOB NOT NULL UNIQUE CHECK(length(token_hash)=32),
    expires_ms INTEGER NOT NULL,
    recovery_pending INTEGER NOT NULL DEFAULT 0 CHECK(recovery_pending IN (0,1)),
    revoked INTEGER NOT NULL DEFAULT 0 CHECK(revoked IN (0,1))
);

CREATE TABLE installation (
    singleton INTEGER PRIMARY KEY CHECK(singleton=1)
);

CREATE TABLE registries (
    id TEXT PRIMARY KEY NOT NULL,
    name TEXT NOT NULL UNIQUE,
    host TEXT NOT NULL,
    repository_prefix TEXT NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
    ciphertext BLOB
);

CREATE TABLE registry_bindings (
    registry_id TEXT NOT NULL REFERENCES registries(id),
    project TEXT NOT NULL,
    agent TEXT NOT NULL REFERENCES agent_certificates(agent),
    target TEXT NOT NULL DEFAULT '',
    PRIMARY KEY(registry_id,project,agent,target)
);

CREATE TABLE node_operation_capabilities (
    agent TEXT PRIMARY KEY,
    generation INTEGER NOT NULL,
    seen_ms INTEGER NOT NULL
);

CREATE TABLE node_operations (
    id TEXT PRIMARY KEY,
    agent TEXT NOT NULL,
    generation INTEGER NOT NULL,
    actor TEXT NOT NULL,
    action TEXT NOT NULL,
    request BLOB NOT NULL,
    mutation INTEGER NOT NULL CHECK (mutation IN (0,1)),
    state TEXT NOT NULL CHECK (state IN ('queued','running','succeeded','failed','uncertain')),
    error TEXT NOT NULL DEFAULT '',
    result BLOB,
    created_ms INTEGER NOT NULL,
    expires_ms INTEGER NOT NULL,
    completed_ms INTEGER,
    restore_fenced INTEGER NOT NULL DEFAULT 0 CHECK(restore_fenced IN (0,1)),
    history_target TEXT NOT NULL DEFAULT '');

CREATE TABLE console_templates (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE COLLATE NOCASE,
    description TEXT NOT NULL
);

CREATE TABLE console_template_versions (
    template_id TEXT NOT NULL REFERENCES console_templates(id),
    version INTEGER NOT NULL CHECK (version > 0),
    compose TEXT NOT NULL,
    digest TEXT NOT NULL,
    parameters TEXT NOT NULL,
    secret_services TEXT NOT NULL,
    created_ms INTEGER NOT NULL,
    PRIMARY KEY (template_id, version)
);

CREATE TABLE node_display_names (
    agent TEXT PRIMARY KEY NOT NULL,
    display_name TEXT NOT NULL CHECK(length(display_name) BETWEEN 1 AND 64)
);

CREATE TABLE container_webhooks (
    id TEXT PRIMARY KEY,
    token_hash BLOB NOT NULL UNIQUE CHECK (length(token_hash) = 32),
    actor TEXT NOT NULL,
    agent TEXT NOT NULL,
    generation INTEGER NOT NULL,
    name TEXT NOT NULL,
    repository TEXT NOT NULL,
    project TEXT NOT NULL,
    target TEXT NOT NULL,
    registry_id TEXT NOT NULL,
    revoked INTEGER NOT NULL DEFAULT 0 CHECK (revoked IN (0,1))
);

CREATE TABLE container_webhook_deliveries (
    webhook TEXT NOT NULL REFERENCES container_webhooks(id),
    delivery_key TEXT NOT NULL,
    image TEXT NOT NULL,
    operation TEXT NOT NULL UNIQUE REFERENCES node_operations(id) ON DELETE CASCADE,
    PRIMARY KEY(webhook,delivery_key)
);

CREATE TABLE ci_container_targets (
    target TEXT PRIMARY KEY REFERENCES targets(id),
    agent TEXT NOT NULL,
    generation INTEGER NOT NULL CHECK (generation > 0),
    name TEXT NOT NULL,
    repository TEXT NOT NULL,
    registry_id TEXT NOT NULL,
    UNIQUE (agent,generation,name)
);

CREATE TABLE ci_container_deliveries (
    credential TEXT NOT NULL REFERENCES service_credentials(id),
    delivery_key TEXT NOT NULL,
    target TEXT NOT NULL REFERENCES ci_container_targets(target),
    image TEXT NOT NULL,
    operation TEXT NOT NULL UNIQUE REFERENCES node_operations(id) ON DELETE CASCADE,
    PRIMARY KEY(credential,target,delivery_key)
);

CREATE TABLE ci_credential_grants (
    credential TEXT NOT NULL REFERENCES service_credentials(id),
    project TEXT NOT NULL,
    target TEXT NOT NULL,
    can_publish INTEGER NOT NULL CHECK (can_publish IN (0,1)),
    PRIMARY KEY (credential, target),
    FOREIGN KEY (project, target) REFERENCES targets(project, id)
);

CREATE TABLE alert_settings (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    cpu_percent INTEGER NOT NULL CHECK (cpu_percent BETWEEN 1 AND 100),
    memory_percent INTEGER NOT NULL CHECK (memory_percent BETWEEN 1 AND 100),
    sustained_ms INTEGER NOT NULL CHECK (sustained_ms BETWEEN 1000 AND 3600000),
    updated_by TEXT NOT NULL,
    updated_ms INTEGER NOT NULL
);

CREATE TABLE alert_targets (
    id TEXT PRIMARY KEY,
    url TEXT NOT NULL,
    created_by TEXT NOT NULL,
    created_ms INTEGER NOT NULL
);

CREATE INDEX target_assignment_peer ON target_assignments(agent, generation);

CREATE INDEX deployment_queue_state ON deployments(state, target);

CREATE INDEX deployment_dispatch_peer ON deployments(agent, generation, state, expires_ms);

CREATE INDEX human_sessions_user ON human_sessions(user_id, expires_ms);

CREATE INDEX recovery_inventory_target ON recovery_inventory(epoch, target, execution_id);

CREATE INDEX target_release_identity ON target_releases(project, id);

CREATE INDEX project_audit_sequence ON project_audit(project,id);

CREATE INDEX deployment_audit_by_deployment ON deployment_audit(deployment_id,id);

CREATE INDEX registries_by_host ON registries(host);

CREATE INDEX registry_bindings_by_scope ON registry_bindings(project,agent,target,registry_id);

CREATE INDEX node_operations_agent_state ON node_operations(agent,state,expires_ms);

CREATE INDEX node_operations_retention ON node_operations(created_ms);

CREATE INDEX node_operation_monitoring ON node_operations(agent,generation,completed_ms)
 WHERE action='container.restart' AND state='succeeded';

CREATE UNIQUE INDEX active_container_webhooks ON container_webhooks(agent,generation,name) WHERE revoked=0;

CREATE INDEX ci_container_deliveries_target ON ci_container_deliveries(target);

CREATE INDEX ci_credential_grants_target ON ci_credential_grants(target);

CREATE INDEX node_operations_history ON node_operations(agent,actor,created_ms DESC) WHERE mutation=1;

CREATE INDEX deployment_audit_by_time ON deployment_audit(created_at);

CREATE INDEX project_audit_by_time ON project_audit(created_at);

CREATE INDEX deployments_by_node ON deployments(agent,generation);

CREATE INDEX deployment_cancellations_pending ON deployment_cancellations(acknowledged) WHERE acknowledged=0;

CREATE VIEW audit_deployment_events AS
SELECT a.id AS source_id, d.project, d.id AS subject, a.created_at,
       CASE WHEN a.id != (SELECT min(first.id) FROM deployment_audit first WHERE first.deployment_id=d.id)
         THEN 'deployment.state_changed'
         WHEN d.rollback_of IS NOT NULL THEN 'deployment.rollback_requested' ELSE 'deployment.requested' END AS action,
       CASE WHEN a.id = (SELECT min(first.id) FROM deployment_audit first WHERE first.deployment_id=d.id)
         THEN d.caller ELSE 'system' END AS actor,
       CASE WHEN a.reason='' THEN a.state ELSE a.state || ': ' || a.reason END AS detail
FROM deployment_audit a JOIN deployments d ON d.id=a.deployment_id;

CREATE VIEW ci_credential_scopes AS
    SELECT id AS credential,project,target,can_publish FROM service_credentials
    UNION ALL
    SELECT credential,project,target,can_publish FROM ci_credential_grants;

CREATE TRIGGER audit_deployment_state AFTER INSERT ON deployment_audit BEGIN
    UPDATE deployments SET last_reason=NEW.reason WHERE id=NEW.deployment_id;
    INSERT INTO project_audit(project,action,actor,subject,detail,created_at)
    SELECT project,action,actor,subject,detail,created_at FROM audit_deployment_events WHERE source_id=NEW.id;
END;

CREATE TRIGGER audit_deployment_cancellation AFTER INSERT ON deployment_cancellations BEGIN
    INSERT INTO project_audit(project,action,actor,subject,detail)
    SELECT project,'deployment.cancellation_requested',NEW.operator,NEW.deployment_id,NEW.note
    FROM deployments WHERE id=NEW.deployment_id;
END;

CREATE TRIGGER audit_ci_credential_issue AFTER INSERT ON service_credentials BEGIN
    INSERT INTO project_audit(project,action,actor,subject,detail)
    VALUES (NEW.project,'ci_credential.issued','local',NEW.id,NEW.target);
END;

CREATE TRIGGER audit_target_register AFTER INSERT ON targets BEGIN
    INSERT INTO project_audit(project,action,actor,subject)
    VALUES (NEW.project,'target.registered','local',NEW.id);
END;

CREATE TRIGGER audit_secret_create AFTER INSERT ON target_secrets BEGIN
    INSERT INTO project_audit(project,action,actor,subject,detail)
    VALUES (NEW.project,'secret.version_created','local',NEW.target,NEW.name || '@' || NEW.version);
END;

CREATE TRIGGER audit_secret_revoke AFTER UPDATE OF revoked ON target_secrets
WHEN OLD.revoked=0 AND NEW.revoked=1 BEGIN
    INSERT INTO project_audit(project,action,actor,subject,detail)
    VALUES (NEW.project,'secret.version_revoked','local',NEW.target,NEW.name || '@' || NEW.version);
END;

CREATE TRIGGER record_installation AFTER INSERT ON human_users BEGIN
    INSERT INTO installation VALUES(1) ON CONFLICT DO NOTHING;
END;
