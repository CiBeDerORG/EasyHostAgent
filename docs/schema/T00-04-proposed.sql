-- T00-04: PROPOSED SCHEMA ONLY -- NOT an active SeaORM migration.
-- Based on EasyTier feat/automation-center at 2026-10-27.
-- Future Rust migration must adapt/reconcile schemas, run a backup, and enforce PRAGMA foreign_keys=ON.
-- Do not execute this file against a live database as-is.
--
-- PRECONDITIONS:
--  users(id), devices(user_id,machine_id), asset_devices(id,owner_user_id,machine_id),
--  asset_services(id,owner_user_id,machine_id,device_id),
--  deploy_agents(id,user_id), deploy_tasks already exist.
--
-- Preserve referenced parents on "delete" by ARCHIVING; do not disable FKs.
ALTER TABLE devices ADD COLUMN inventory_archived_at TEXT;
ALTER TABLE asset_devices ADD COLUMN archived_at TEXT;

-- Necessary for composite ownership FK; id is already the primary key.
CREATE UNIQUE INDEX uq_deploy_agents_owner_key ON deploy_agents(id, user_id);

-- One persistent local host (deploy_agents.id) can be paired to at most one device at a time.
-- Unbinding sets machine_id/asset_device_id NULL while retaining host id, agent id,
-- tasks and managed applications. Old bearer token MUST be erased in deploy_agents.credential.
CREATE TABLE host_agent_bindings (
    connection_id TEXT PRIMARY KEY NOT NULL,
    owner_user_id INTEGER NOT NULL,
    agent_id TEXT NOT NULL,
    machine_id TEXT,
    asset_device_id TEXT,
    binding_state TEXT NOT NULL CHECK (binding_state IN ('bound', 'unbound')),
    binding_generation INTEGER NOT NULL DEFAULT 1 CHECK (binding_generation >= 1),
    tls_spki_sha256 TEXT NOT NULL CHECK (length(tls_spki_sha256) = 64),
    paired_at TEXT,
    unbound_at TEXT,
    last_machine_id TEXT,
    last_asset_device_id TEXT,
    remote_revoked INTEGER NOT NULL DEFAULT 0 CHECK (remote_revoked IN (0, 1)),
    updated_at TEXT NOT NULL,
    CHECK (
        (binding_state = 'bound' AND machine_id IS NOT NULL AND asset_device_id IS NOT NULL AND paired_at IS NOT NULL)
        OR
        (binding_state = 'unbound' AND machine_id IS NULL AND asset_device_id IS NULL)
    ),
    FOREIGN KEY (connection_id, owner_user_id)
        REFERENCES deploy_agents(id, user_id) ON DELETE RESTRICT,
    FOREIGN KEY (asset_device_id, owner_user_id, machine_id)
        REFERENCES asset_devices(id, owner_user_id, machine_id) ON DELETE RESTRICT
);
CREATE UNIQUE INDEX uq_host_agent_active_identity
    ON host_agent_bindings(agent_id) WHERE binding_state = 'bound';
CREATE UNIQUE INDEX uq_host_agent_active_asset_device
    ON host_agent_bindings(owner_user_id, machine_id, asset_device_id)
    WHERE binding_state = 'bound';
CREATE INDEX idx_host_agent_owner
    ON host_agent_bindings(owner_user_id, binding_state);

-- Application is NOT a child of asset_devices: deleting or archiving that device
-- must never cascade away its managed app history. All app data belongs to the
-- stable host connection/identity, not to a replaceable IP address.
CREATE TABLE host_app_instances (
    id TEXT PRIMARY KEY NOT NULL,
    host_connection_id TEXT NOT NULL,
    owner_user_id INTEGER NOT NULL,
    app_id TEXT NOT NULL,
    source_kind TEXT NOT NULL CHECK (source_kind IN ('deploy', 'adopted')),
    compose_project TEXT,
    docker_resource_id TEXT,
    package_sha256 TEXT,
    catalog_revision TEXT,
    software_root_at_install TEXT NOT NULL,
    config_cipher TEXT,
    management_state TEXT NOT NULL
        CHECK (management_state IN ('installing','managed','uninstalling','uninstalled','failed','unknown')),
    last_observed_state TEXT NOT NULL DEFAULT 'unknown'
        CHECK (last_observed_state IN ('running','stopped','partial','unknown')),
    last_observed_at TEXT,
    last_error TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE (host_connection_id, app_id),
    UNIQUE (id, owner_user_id),
    FOREIGN KEY (host_connection_id, owner_user_id)
        REFERENCES deploy_agents(id, user_id) ON DELETE RESTRICT
);
CREATE INDEX idx_host_app_owner ON host_app_instances(owner_user_id, host_connection_id);
CREATE INDEX idx_host_app_compose ON host_app_instances(host_connection_id, compose_project);

-- Explicit ownership for all services linked to an application.
-- Service can belong to ONE app at most; an app may have many linked services.
-- Deleting an asset service by its own normal API removes this link only.
-- Deleting an app while links remain is intentionally blocked by ON DELETE RESTRICT.
CREATE TABLE host_app_service_links (
    instance_id TEXT NOT NULL,
    owner_user_id INTEGER NOT NULL,
    service_id TEXT NOT NULL,
    machine_id TEXT NOT NULL,
    asset_device_id TEXT NOT NULL,
    is_primary INTEGER NOT NULL DEFAULT 0 CHECK (is_primary IN (0, 1)),
    link_origin TEXT NOT NULL CHECK (link_origin IN ('automatic','user')),
    sync_state TEXT NOT NULL DEFAULT 'synced'
        CHECK (sync_state IN ('synced','pending','failed','stale')),
    sync_error TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (instance_id, service_id),
    UNIQUE (service_id),
    FOREIGN KEY (instance_id, owner_user_id)
        REFERENCES host_app_instances(id, owner_user_id) ON DELETE RESTRICT,
    FOREIGN KEY (service_id, owner_user_id, machine_id, asset_device_id)
        REFERENCES asset_services(id, owner_user_id, machine_id, device_id)
        ON DELETE CASCADE
);
CREATE UNIQUE INDEX uq_host_app_one_primary
    ON host_app_service_links(instance_id) WHERE is_primary = 1;
CREATE INDEX idx_host_app_service_scope
    ON host_app_service_links(owner_user_id, machine_id, asset_device_id);

-- Audit log: never stores bearer tokens, pairing codes or plaintext secrets.
CREATE TABLE host_binding_events (
    id TEXT PRIMARY KEY NOT NULL,
    host_connection_id TEXT NOT NULL,
    owner_user_id INTEGER NOT NULL,
    agent_id TEXT NOT NULL,
    event_type TEXT NOT NULL
        CHECK (event_type IN ('paired','unbound','repaired','remote_revoke_failed','archived')),
    actor_user_id INTEGER,
    old_device_id TEXT,
    new_device_id TEXT,
    happened_at TEXT NOT NULL,
    FOREIGN KEY (host_connection_id, owner_user_id)
        REFERENCES deploy_agents(id, user_id) ON DELETE RESTRICT
);
CREATE INDEX idx_host_binding_events
    ON host_binding_events(host_connection_id, happened_at);

-- Future migration tests MUST execute:
--   PRAGMA foreign_key_check;
--   SELECT name FROM sqlite_master WHERE type='index' AND name LIKE 'uq_host_%';
-- DO NOT DROP deploy_agents ON UNBIND:
-- current deploy_tasks(connection_id) ON DELETE CASCADE would erase task history.
-- Must change legacy DELETE /api/v1/deploy/agents/{id} to an authorization unbind/retire.
