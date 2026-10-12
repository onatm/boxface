CREATE TABLE bootstrap_codes (
    hash TEXT PRIMARY KEY,
    owner_id TEXT NOT NULL REFERENCES owners (id),
    expires_at TEXT NOT NULL,
    used_at TEXT
);

CREATE TABLE api_tokens (
    id TEXT PRIMARY KEY,
    owner_id TEXT NOT NULL REFERENCES owners (id),
    hash TEXT NOT NULL UNIQUE,
    created_at TEXT NOT NULL,
    revoked_at TEXT
);
