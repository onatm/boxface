ALTER TABLE api_tokens ADD COLUMN name TEXT NOT NULL DEFAULT '';
ALTER TABLE api_tokens ADD COLUMN last_used_at TEXT;

CREATE TABLE auth_events (
    id INTEGER PRIMARY KEY,
    owner_id TEXT NOT NULL REFERENCES owners (id),
    action TEXT NOT NULL,
    token_id TEXT NOT NULL,
    remote TEXT NOT NULL,
    created_at TEXT NOT NULL
);
