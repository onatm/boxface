CREATE TABLE secrets (
    id TEXT PRIMARY KEY,
    owner_id TEXT NOT NULL REFERENCES owners (id),
    name TEXT NOT NULL,
    ciphertext BLOB NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE (owner_id, name)
);

CREATE TABLE secret_bindings (
    secret_id TEXT NOT NULL REFERENCES secrets (id) ON DELETE CASCADE,
    owner_id TEXT NOT NULL REFERENCES owners (id),
    consumer TEXT NOT NULL,
    variable TEXT NOT NULL,
    UNIQUE (owner_id, consumer, variable)
);
