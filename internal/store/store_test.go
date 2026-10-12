package store

import (
	"database/sql"
	"io/fs"
	"path/filepath"
	"sort"
	"testing"
)

func TestOpenAppliesMigrationsOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "boxface.db")
	for i := 0; i < 2; i++ {
		db, err := Open(path)
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		var applied int
		if err := db.QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&applied); err != nil {
			t.Fatal(err)
		}
		if applied != 14 {
			t.Fatalf("open %d: %d migrations recorded", i, applied)
		}
		var mode string
		if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
			t.Fatal(err)
		}
		if mode != "wal" {
			t.Fatalf("journal mode = %s", mode)
		}
		db.Close()
	}
}

func openBefore(t *testing.T, path, version string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE schema_migrations (version TEXT PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	names, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(names)
	for _, name := range names {
		if name >= "migrations/"+version {
			break
		}
		body, err := migrations.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(body)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if _, err := db.Exec(`INSERT INTO schema_migrations (version) VALUES (?)`, name); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func TestTemplatesMigrationKeepsSavedDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "boxface.db")
	db := openBefore(t, path, "0013")
	for _, q := range []string{
		`INSERT INTO owners (id, name, created_at) VALUES ('o1', 'owner', '2026-10-01T00:00:00Z')`,
		`INSERT INTO owner_defaults (owner_id, image, vcpus, memory_mib, disk_gib, profile, updated_at) VALUES ('o1', 'next', 4, 8192, 40, 'tools', '2026-10-02T00:00:00Z')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()

	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var name, image, profile string
	var vcpus, memory, disk int
	if err := db.QueryRow(`SELECT name, image, profile, vcpus, memory_mib, disk_gib FROM templates WHERE owner_id = 'o1'`).
		Scan(&name, &image, &profile, &vcpus, &memory, &disk); err != nil {
		t.Fatal(err)
	}
	if name != "default" || image != "next" || profile != "tools" || vcpus != 4 || memory != 8192 || disk != 40 {
		t.Fatalf("template = %s %s %s %d %d %d", name, image, profile, vcpus, memory, disk)
	}
	if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE name = 'owner_defaults'`).Scan(&name); err != sql.ErrNoRows {
		t.Fatalf("owner_defaults still exists: %v", err)
	}
}

func TestDevicesMigrationLinksKeysByName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "boxface.db")
	db := openBefore(t, path, "0014")
	for _, q := range []string{
		`INSERT INTO owners (id, name, created_at) VALUES ('o1', 'one', '2026-10-01T00:00:00Z'), ('o2', 'two', '2026-10-01T00:00:00Z')`,
		`INSERT INTO api_tokens (id, owner_id, hash, created_at, name) VALUES
			('t-old', 'o1', 'h1', '2026-10-01T00:00:00Z', 'tablet'),
			('t-new', 'o1', 'h2', '2026-10-05T00:00:00Z', 'tablet'),
			('t-other', 'o2', 'h3', '2026-10-05T00:00:00Z', 'laptop')`,
		`INSERT INTO api_tokens (id, owner_id, hash, created_at, name, revoked_at) VALUES ('t-revoked', 'o1', 'h4', '2026-10-06T00:00:00Z', 'phone', '2026-10-07T00:00:00Z')`,
		`INSERT INTO ssh_keys (id, owner_id, name, public_key, fingerprint, created_at) VALUES
			('k-tablet', 'o1', 'tablet', 'ssh-ed25519 A', 'SHA256:a', '2026-10-05T00:00:00Z'),
			('k-laptop', 'o1', 'laptop', 'ssh-ed25519 B', 'SHA256:b', '2026-10-05T00:00:00Z'),
			('k-phone', 'o1', 'phone', 'ssh-ed25519 C', 'SHA256:c', '2026-10-06T00:00:00Z')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()

	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	want := map[string]string{"k-tablet": "t-new", "k-laptop": "", "k-phone": ""}
	for key, token := range want {
		var got string
		if err := db.QueryRow(`SELECT coalesce(token_id, '') FROM ssh_keys WHERE id = ?`, key).Scan(&got); err != nil || got != token {
			t.Fatalf("%s linked to %q, %v; want %q", key, got, err, token)
		}
	}
}
