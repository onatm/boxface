package auth

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/onatm/boxface/internal/store"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "boxface.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return New(db)
}

func TestBootstrapCodeIssuesTokenOnce(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	code, err := s.Bootstrap(ctx, "onat", 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	token, owner, err := s.Login(ctx, code)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Authenticate(ctx, token)
	if err != nil || got != owner {
		t.Fatalf("authenticate = %q, %v; want %q", got, err, owner)
	}
	if _, _, err := s.Login(ctx, code); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("second login with the same code: err = %v", err)
	}
	var stored int
	s.db.QueryRow(`SELECT count(*) FROM api_tokens WHERE hash = ?`, token).Scan(&stored)
	if stored != 0 {
		t.Fatal("token stored in plaintext")
	}
}

func TestBootstrapReusesOwner(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	first, _ := s.Bootstrap(ctx, "onat", time.Minute)
	second, _ := s.Bootstrap(ctx, "onat", time.Minute)
	_, a, _ := s.Login(ctx, first)
	_, b, _ := s.Login(ctx, second)
	if a == "" || a != b {
		t.Fatalf("owners = %q, %q", a, b)
	}
}

func TestExpiredCodeRejected(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	code, _ := s.Bootstrap(ctx, "onat", time.Minute)
	s.now = func() time.Time { return time.Now().Add(2 * time.Minute) }
	if _, _, err := s.Login(ctx, code); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v", err)
	}
}

func TestRevokedTokenRejected(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	code, _ := s.Bootstrap(ctx, "onat", time.Minute)
	token, _, _ := s.Login(ctx, code)
	if n, err := s.RevokeAll(ctx, "onat"); err != nil || n != 1 {
		t.Fatalf("revoked %d, %v", n, err)
	}
	if _, err := s.Authenticate(ctx, token); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v", err)
	}
	if _, err := s.Authenticate(ctx, "bfx_unknown"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("unknown token: err = %v", err)
	}
}
