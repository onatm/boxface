ALTER TABLE environments ADD COLUMN profile_revision_id TEXT REFERENCES profile_revisions (id);
ALTER TABLE owner_defaults ADD COLUMN profile TEXT NOT NULL DEFAULT 'main';
