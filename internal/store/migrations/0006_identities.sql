CREATE TABLE identities (
    id TEXT PRIMARY KEY,
    owner_id TEXT NOT NULL REFERENCES owners (id),
    provider TEXT NOT NULL,
    subject TEXT NOT NULL,
    created_at TEXT NOT NULL,
    UNIQUE (provider, subject)
);
