ALTER TABLE images ADD COLUMN removed_at TEXT;
ALTER TABLE profiles ADD COLUMN deleted_at TEXT;
ALTER TABLE profile_revisions ADD COLUMN pruned_at TEXT;
