package secrets

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/onatm/boxface/internal/store"
)

const claudeToken = "sk-ant-oat01-example-token-value"

func newStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "boxface.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`INSERT INTO owners (id, name, created_at) VALUES ('owner-1', 'one', 'x'), ('owner-2', 'two', 'x')`); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(dir, "secrets.key")
	keeper, err := OpenKeeper(context.Background(), "file://"+keyPath)
	if err != nil {
		t.Fatal(err)
	}
	return NewStore(db, keeper), keyPath
}

var claudeBinding = []Binding{{"claude-code", "CLAUDE_CODE_OAUTH_TOKEN"}}

func TestCreateEncryptsAndResolves(t *testing.T) {
	s, keyPath := newStore(t)
	ctx := context.Background()
	if info, err := os.Stat(keyPath); err != nil || info.Mode().Perm() != 0o600 || info.Size() != 32 {
		t.Fatalf("key file = %v, %v", info, err)
	}
	sec, err := s.Create(ctx, "owner-1", "claude", claudeToken, claudeBinding)
	if err != nil {
		t.Fatal(err)
	}
	var ciphertext []byte
	s.db.QueryRow(`SELECT ciphertext FROM secrets WHERE id = ?`, sec.ID).Scan(&ciphertext)
	if len(ciphertext) == 0 || bytes.Contains(ciphertext, []byte(claudeToken)) {
		t.Fatalf("ciphertext holds the value or is empty: %d bytes", len(ciphertext))
	}
	r, err := s.Resolve(ctx, "owner-1")
	if err != nil {
		t.Fatal(err)
	}
	if r.Env["claude-code"]["CLAUDE_CODE_OAUTH_TOKEN"] != claudeToken || len(r.Secrets) != 1 || r.Secrets[0] != "claude" {
		t.Fatalf("resolved = %+v", r)
	}
	if other, err := s.Resolve(ctx, "owner-2"); err != nil || len(other.Env) != 0 {
		t.Fatalf("other owner resolved %+v, %v", other, err)
	}
	list, err := s.List(ctx, "owner-1")
	if err != nil || len(list) != 1 || len(list[0].Bindings) != 1 || list[0].Name != "claude" {
		t.Fatalf("list = %+v, %v", list, err)
	}
}

func TestValidation(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	for name, c := range map[string]struct {
		name, value string
		bindings    []Binding
	}{
		"bad name":         {"Claude", claudeToken, claudeBinding},
		"empty value":      {"claude", "", claudeBinding},
		"nul in value":     {"claude", "a\x00b", claudeBinding},
		"oversized value":  {"claude", string(make([]byte, maxValue+1)), claudeBinding},
		"unknown consumer": {"claude", claudeToken, []Binding{{"shell", "TOKEN"}}},
		"lowercase var":    {"claude", claudeToken, []Binding{{"pi", "token"}}},
		"PATH":             {"claude", claudeToken, []Binding{{"pi", "PATH"}}},
		"LD_PRELOAD":       {"claude", claudeToken, []Binding{{"pi", "LD_PRELOAD"}}},
		"duplicate":        {"claude", claudeToken, []Binding{{"pi", "A"}, {"pi", "A"}}},
	} {
		if _, err := s.Create(ctx, "owner-1", c.name, c.value, c.bindings); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s: err = %v", name, err)
		}
	}
	if _, err := s.Create(ctx, "owner-1", "claude", claudeToken, claudeBinding); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, "owner-1", "claude", "other", nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate name: err = %v", err)
	}
	if _, err := s.Create(ctx, "owner-1", "claude-two", "other", claudeBinding); !errors.Is(err, ErrConflict) {
		t.Fatalf("variable bound twice: err = %v", err)
	}
	if list, _ := s.List(ctx, "owner-1"); len(list) != 1 {
		t.Fatalf("rejected creates left %+v", list)
	}
	if _, err := s.Create(ctx, "owner-2", "claude", claudeToken, claudeBinding); err != nil {
		t.Fatalf("another owner cannot reuse the name and variable: %v", err)
	}
}

func TestUpdateAndDelete(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	sec, err := s.Create(ctx, "owner-1", "openai", "sk-one", []Binding{{"codex", "OPENAI_API_KEY"}})
	if err != nil {
		t.Fatal(err)
	}
	both := []Binding{{"codex", "OPENAI_API_KEY"}, {"opencode", "OPENAI_API_KEY"}}
	if got, err := s.Update(ctx, "owner-1", sec.ID, nil, &both); err != nil || len(got.Bindings) != 2 {
		t.Fatalf("rebind = %+v, %v", got, err)
	}
	if r, _ := s.Resolve(ctx, "owner-1"); r.Env["opencode"]["OPENAI_API_KEY"] != "sk-one" {
		t.Fatalf("value lost when only bindings changed: %+v", r)
	}
	value := "sk-two"
	if _, err := s.Update(ctx, "owner-1", sec.ID, &value, nil); err != nil {
		t.Fatal(err)
	}
	if r, _ := s.Resolve(ctx, "owner-1"); r.Env["codex"]["OPENAI_API_KEY"] != "sk-two" || len(r.Env) != 2 {
		t.Fatalf("after replacing the value: %+v", r)
	}
	if _, err := s.Update(ctx, "owner-2", sec.ID, &value, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update by another owner: err = %v", err)
	}
	if _, err := s.Delete(ctx, "owner-2", sec.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete by another owner: err = %v", err)
	}
	if name, err := s.Delete(ctx, "owner-1", sec.ID); err != nil || name != "openai" {
		t.Fatalf("delete = %q, %v", name, err)
	}
	if r, _ := s.Resolve(ctx, "owner-1"); len(r.Env) != 0 {
		t.Fatalf("bindings survived delete: %+v", r)
	}
}

func TestWrongKeyFailsClearly(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	if _, err := s.Create(ctx, "owner-1", "claude", claudeToken, claudeBinding); err != nil {
		t.Fatal(err)
	}
	other, err := OpenKeeper(ctx, "file://"+filepath.Join(t.TempDir(), "other.key"))
	if err != nil {
		t.Fatal(err)
	}
	wrong := NewStore(s.db, other)
	if list, err := wrong.List(ctx, "owner-1"); err != nil || len(list) != 1 {
		t.Fatalf("list with another key = %+v, %v", list, err)
	}
	if _, err := wrong.Resolve(ctx, "owner-1"); !errors.Is(err, ErrKey) {
		t.Fatalf("resolve with another key: err = %v", err)
	}
	bad := filepath.Join(t.TempDir(), "short.key")
	os.WriteFile(bad, []byte("short"), 0o600)
	if _, err := OpenKeeper(ctx, "file://"+bad); err == nil {
		t.Fatal("a short key file was accepted")
	}
}
