CREATE TABLE image_aliases (
    name TEXT PRIMARY KEY,
    image_id TEXT NOT NULL REFERENCES images (id),
    updated_at TEXT NOT NULL
);

CREATE TABLE owner_defaults (
    owner_id TEXT PRIMARY KEY REFERENCES owners (id),
    image TEXT NOT NULL,
    vcpus INTEGER NOT NULL,
    memory_mib INTEGER NOT NULL,
    disk_gib INTEGER NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE ssh_keys (
    id TEXT PRIMARY KEY,
    owner_id TEXT NOT NULL REFERENCES owners (id),
    name TEXT NOT NULL,
    public_key TEXT NOT NULL,
    fingerprint TEXT NOT NULL,
    created_at TEXT NOT NULL,
    UNIQUE (owner_id, fingerprint)
);
