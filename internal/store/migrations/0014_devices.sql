ALTER TABLE ssh_keys ADD COLUMN token_id TEXT REFERENCES api_tokens (id);
UPDATE ssh_keys SET token_id = (
    SELECT t.id FROM api_tokens t
    WHERE t.owner_id = ssh_keys.owner_id AND t.name = ssh_keys.name AND t.revoked_at IS NULL
    ORDER BY t.created_at DESC, t.rowid DESC LIMIT 1
);
