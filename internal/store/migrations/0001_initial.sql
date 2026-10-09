CREATE TABLE owners (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    created_at TEXT NOT NULL
);

CREATE TABLE images (
    id TEXT PRIMARY KEY,
    digest TEXT NOT NULL UNIQUE,
    status TEXT NOT NULL CHECK (status IN ('ready', 'failed')),
    created_at TEXT NOT NULL
);

CREATE TABLE environments (
    id TEXT PRIMARY KEY,
    owner_id TEXT NOT NULL REFERENCES owners (id),
    name TEXT NOT NULL,
    image_id TEXT NOT NULL REFERENCES images (id),
    vcpus INTEGER NOT NULL,
    memory_mib INTEGER NOT NULL,
    desired_state TEXT NOT NULL CHECK (desired_state IN ('running', 'stopped', 'deleted')),
    observed_state TEXT NOT NULL CHECK (observed_state IN ('pending', 'starting', 'running', 'stopping', 'stopped', 'deleting', 'deleted', 'error')),
    error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE UNIQUE INDEX environments_owner_name ON environments (owner_id, name) WHERE observed_state <> 'deleted';

CREATE TABLE data_disks (
    id TEXT PRIMARY KEY,
    owner_id TEXT NOT NULL REFERENCES owners (id),
    environment_id TEXT NOT NULL REFERENCES environments (id),
    size_gib INTEGER NOT NULL,
    retention TEXT NOT NULL CHECK (retention IN ('attached', 'retained', 'purged')),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE operations (
    id TEXT PRIMARY KEY,
    owner_id TEXT NOT NULL REFERENCES owners (id),
    type TEXT NOT NULL,
    target_id TEXT NOT NULL,
    idempotency_key TEXT,
    request_hash TEXT NOT NULL,
    params TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('queued', 'running', 'succeeded', 'failed', 'cancelled')),
    error_code TEXT NOT NULL DEFAULT '',
    error_message TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE UNIQUE INDEX operations_idempotency ON operations (owner_id, type, idempotency_key) WHERE idempotency_key IS NOT NULL;

CREATE TABLE operation_steps (
    operation_id TEXT NOT NULL REFERENCES operations (id),
    step TEXT NOT NULL,
    completed_at TEXT NOT NULL,
    PRIMARY KEY (operation_id, step)
);

CREATE TABLE audit_events (
    id INTEGER PRIMARY KEY,
    owner_id TEXT NOT NULL REFERENCES owners (id),
    action TEXT NOT NULL,
    target_id TEXT NOT NULL,
    operation_id TEXT NOT NULL REFERENCES operations (id),
    created_at TEXT NOT NULL
);
