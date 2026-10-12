package profiles

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/onatm/boxface/internal/store"
)

func TestNormalize(t *testing.T) {
	s, err := Spec{Packages: []string{" ripgrep", "nodejs_22", "ripgrep", "tmux", "python3Packages.requests"}}.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(s.Packages, " ") != "nodejs_22 python3Packages.requests ripgrep" {
		t.Fatalf("packages = %q", s.Packages)
	}
	for name, bad := range map[string]Spec{
		"shell injection":       {Packages: []string{"ripgrep; rm -rf /"}},
		"nix interpolation":     {Packages: []string{"${builtins.readFile /etc/passwd}"}},
		"quote":                 {Packages: []string{`"x"`}},
		"leading dot":           {Packages: []string{".hidden"}},
		"packages and module":   {Packages: []string{"jq"}, Module: "{ }"},
		"huge module":           {Module: strings.Repeat("x", maxModule+1)},
		"http dotfiles":         {Dotfiles: &Dotfiles{Repo: "http://example.com/d.git"}},
		"credentials in URL":    {Dotfiles: &Dotfiles{Repo: "https://user:pass@example.com/d.git"}},
		"file dotfiles":         {Dotfiles: &Dotfiles{Repo: "file:///etc"}},
		"ref escaping":          {Dotfiles: &Dotfiles{Repo: "https://example.com/d.git", Ref: "../../x"}},
		"ref with option shape": {Dotfiles: &Dotfiles{Repo: "https://example.com/d.git", Ref: "--upload-pack=x"}},
	} {
		if _, err := bad.Normalize(); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s: err = %v", name, err)
		}
	}
	if _, err := (Spec{Module: "{ pkgs, ... }: { home.packages = [ pkgs.jq ]; }", Dotfiles: &Dotfiles{Repo: "https://github.com/onatm/dotfiles", Ref: "main"}}).Normalize(); err != nil {
		t.Fatalf("valid module spec: %v", err)
	}
}

func TestBuildKey(t *testing.T) {
	a, _ := Spec{Packages: []string{"jq", "ripgrep"}}.Normalize()
	b, _ := Spec{Packages: []string{"ripgrep", "jq", "jq", "git"}}.Normalize()
	if BuildKey("owner-1", a) != BuildKey("owner-1", b) {
		t.Fatal("equal specs have different build keys")
	}
	c, _ := Spec{Packages: []string{"jq", "ripgrep"}, Unfree: true}.Normalize()
	for name, other := range map[string]string{"another owner": BuildKey("owner-2", a), "unfree": BuildKey("owner-1", c)} {
		if other == BuildKey("owner-1", a) {
			t.Fatalf("%s shares a build key", name)
		}
	}
}

func TestFlake(t *testing.T) {
	picker, _ := Spec{Packages: []string{"ripgrep", "python3Packages.requests"}, Unfree: true}.Normalize()
	files := Flake(picker)
	f := files["flake.nix"]
	for _, want := range []string{
		`nixpkgs.url = "github:NixOS/nixpkgs/7c8764b7c7b09b34f632464276218ef9090eaa11"`,
		`url = "github:nix-community/home-manager/db7d5e2332710f5abb088f6b5de927d7f9511b35"`,
		"config.allowUnfree = true;",
		"home.packages = with pkgs; [ git tmux python3Packages.requests ripgrep ];",
	} {
		if !strings.Contains(f, want) {
			t.Fatalf("flake.nix lacks %q:\n%s", want, f)
		}
	}
	if strings.Contains(f, "user.nix") || files["user.nix"] != "" || !strings.Contains(files["flake.lock"], `"version": 7`) {
		t.Fatalf("picker files = %v", files)
	}
	raw, _ := Spec{Module: "{ pkgs, ... }: { home.packages = [ pkgs.jq ]; }"}.Normalize()
	files = Flake(raw)
	if !strings.Contains(files["flake.nix"], "./user.nix") || files["user.nix"] != raw.Module || !strings.Contains(files["flake.nix"], "allowUnfree = false") {
		t.Fatalf("module files = %v", files)
	}
}

func newStore(t *testing.T) *Store {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "boxface.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`INSERT INTO owners (id, name, created_at) VALUES ('owner-1', 'one', 'x'), ('owner-2', 'two', 'x')`); err != nil {
		t.Fatal(err)
	}
	return NewStore(db)
}

func TestProfilesAndRevisions(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	p, err := s.Create(ctx, "owner-1", "main", Spec{Packages: []string{"ripgrep"}})
	if err != nil {
		t.Fatal(err)
	}
	if p.Latest == nil || p.Latest.Number != 1 || p.Latest.State != "pending" {
		t.Fatalf("created = %+v", p)
	}
	if _, err := s.Create(ctx, "owner-1", "main", Spec{}); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("duplicate name: err = %v", err)
	}
	if _, err := s.Create(ctx, "owner-1", "Main", Spec{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad name: err = %v", err)
	}
	if _, err := s.Create(ctx, "owner-2", "main", Spec{}); err != nil {
		t.Fatalf("another owner cannot use the same name: %v", err)
	}
	second, err := s.AddRevision(ctx, "owner-1", p.ID, Spec{Packages: []string{"ripgrep", "jq"}})
	if err != nil || second.Number != 2 {
		t.Fatalf("second revision = %+v, %v", second, err)
	}
	if _, err := s.AddRevision(ctx, "owner-2", p.ID, Spec{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revision on another owner's profile: err = %v", err)
	}
	if _, err := s.Get(ctx, "owner-2", p.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get another owner's profile: err = %v", err)
	}
	got, err := s.Get(ctx, "owner-1", p.ID)
	if err != nil || len(got.Revisions) != 2 || got.Latest.Number != 2 || strings.Join(got.Revisions[1].Spec.Packages, ",") != "ripgrep" {
		t.Fatalf("get = %+v, %v", got, err)
	}
	list, err := s.List(ctx, "owner-1")
	if err != nil || len(list) != 1 || list[0].Latest.Number != 2 {
		t.Fatalf("list = %+v, %v", list, err)
	}

	if _, err := s.Ready(ctx, "owner-1", "main"); !errors.Is(err, ErrNotReady) {
		t.Fatalf("ready before a build: err = %v", err)
	}
	if _, err := s.Ready(ctx, "owner-1", "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ready for a missing profile: err = %v", err)
	}
	s.db.Exec(`UPDATE profile_revisions SET state = 'ready' WHERE id = ?`, p.Latest.ID)
	if rev, err := s.Ready(ctx, "owner-1", "main"); err != nil || rev.Number != 1 {
		t.Fatalf("ready = %+v, %v", rev, err)
	}
	if _, err := s.Ready(ctx, "owner-2", "main"); !errors.Is(err, ErrNotReady) {
		t.Fatalf("another owner's ready revision leaked: err = %v", err)
	}
}
