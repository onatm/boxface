CREATE TABLE profiles (
    id TEXT PRIMARY KEY,
    owner_id TEXT NOT NULL REFERENCES owners (id),
    name TEXT NOT NULL,
    created_at TEXT NOT NULL,
    UNIQUE (owner_id, name)
);

CREATE TABLE profile_revisions (
    id TEXT PRIMARY KEY,
    profile_id TEXT NOT NULL REFERENCES profiles (id),
    number INTEGER NOT NULL,
    spec TEXT NOT NULL,
    build_key TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('pending', 'building', 'ready', 'failed')),
    error TEXT NOT NULL DEFAULT '',
    log_tail TEXT NOT NULL DEFAULT '',
    dotfiles_commit TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE (profile_id, number)
);

CREATE INDEX profile_revisions_build_key ON profile_revisions (build_key, state);
