CREATE TABLE templates (
    owner_id TEXT NOT NULL REFERENCES owners (id),
    name TEXT NOT NULL,
    image TEXT NOT NULL,
    profile TEXT NOT NULL,
    vcpus INTEGER NOT NULL,
    memory_mib INTEGER NOT NULL,
    disk_gib INTEGER NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (owner_id, name)
);
INSERT INTO templates (owner_id, name, image, profile, vcpus, memory_mib, disk_gib, updated_at)
    SELECT owner_id, 'default', image, profile, vcpus, memory_mib, disk_gib, updated_at FROM owner_defaults;
DROP TABLE owner_defaults;
