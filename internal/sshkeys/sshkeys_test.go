package sshkeys

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/onatm/boxface/internal/store"
)

const (
	key1 = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIPv/3an15p2WS8pllhCNT8+Ui5iCTCbODBLNGN4kKUba"
	key2 = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIPt5LCJRq9y4c8J1YZeCdDw7TP7B5LlgxGVVvhVUhQuN"
)

func TestParseNormalisesAndFingerprints(t *testing.T) {
	publicKey, fingerprint, err := Parse("  " + key1 + " onat@tablet\n")
	if err != nil {
		t.Fatal(err)
	}
	if publicKey != key1 {
		t.Fatalf("public key = %q", publicKey)
	}
	if len(fingerprint) != len("SHA256:")+43 || fingerprint[:7] != "SHA256:" {
		t.Fatalf("fingerprint = %q", fingerprint)
	}
}

func TestParseRejectsUnsafeInput(t *testing.T) {
	for name, line := range map[string]string{
		"options prefix": `command="/bin/sh" ` + key1,
		"two keys":       key1 + "\n" + key2,
		"unknown type":   "ssh-dss AAAAB3NzaC1kc3M=",
		"bad base64":     "ssh-ed25519 !!!",
		"type mismatch":  "ssh-rsa " + key1[len("ssh-ed25519 "):],
		"empty":          "",
	} {
		if _, _, err := Parse(line); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s: err = %v", name, err)
		}
	}
}

func TestStoreAddIsIdempotentAndOwnerScoped(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "boxface.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`INSERT INTO owners (id, name, created_at) VALUES ('owner-1', 'one', '2026-10-10T00:00:00Z'), ('owner-2', 'two', '2026-10-10T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	s, ctx := NewStore(db), context.Background()

	first, created, err := s.Add(ctx, "owner-1", "", "tablet", key1+" comment")
	if err != nil || !created {
		t.Fatalf("add = %+v, %v, %v", first, created, err)
	}
	again, created, err := s.Add(ctx, "owner-1", "", "renamed", key1)
	if err != nil || created || again.ID != first.ID || again.Name != "tablet" {
		t.Fatalf("add again = %+v, %v, %v", again, created, err)
	}
	if _, _, err := s.Add(ctx, "owner-1", "", "", key2); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty name: err = %v", err)
	}
	if err := s.Delete(ctx, "owner-2", first.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other owner's delete: err = %v", err)
	}
	if list, _ := s.List(ctx, "owner-2"); len(list) != 0 {
		t.Fatalf("other owner sees %+v", list)
	}
	if err := s.Delete(ctx, "owner-1", first.ID); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.List(ctx, "owner-1"); len(list) != 0 {
		t.Fatalf("after delete: %+v", list)
	}
}
