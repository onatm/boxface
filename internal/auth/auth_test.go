package auth

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
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
	token, owner, err := s.Login(ctx, code, "tablet", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	got, _, err := s.Authenticate(ctx, token)
	if err != nil || got != owner {
		t.Fatalf("authenticate = %q, %v; want %q", got, err, owner)
	}
	if _, _, err := s.Login(ctx, code, "tablet", "127.0.0.1"); !errors.Is(err, ErrUnauthorized) {
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
	_, a, _ := s.Login(ctx, first, "tablet", "127.0.0.1")
	_, b, _ := s.Login(ctx, second, "tablet", "127.0.0.1")
	if a == "" || a != b {
		t.Fatalf("owners = %q, %q", a, b)
	}
}

func TestExpiredCodeRejected(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	code, _ := s.Bootstrap(ctx, "onat", time.Minute)
	s.now = func() time.Time { return time.Now().Add(2 * time.Minute) }
	if _, _, err := s.Login(ctx, code, "tablet", "127.0.0.1"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v", err)
	}
}

func TestRevokedTokenRejected(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	code, _ := s.Bootstrap(ctx, "onat", time.Minute)
	token, _, _ := s.Login(ctx, code, "tablet", "127.0.0.1")
	if n, err := s.RevokeAll(ctx, "onat"); err != nil || n != 1 {
		t.Fatalf("revoked %d, %v", n, err)
	}
	if _, _, err := s.Authenticate(ctx, token); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v", err)
	}
	if _, _, err := s.Authenticate(ctx, "bfx_unknown"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("unknown token: err = %v", err)
	}
}

func TestTokensAreNamedAndRevokedIndividually(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	start := time.Now()
	s.now = func() time.Time { return start }
	codeA, _ := s.Bootstrap(ctx, "onat", time.Minute)
	codeB, _ := s.Bootstrap(ctx, "onat", time.Minute)
	tablet, owner, _ := s.Login(ctx, codeA, "tablet", "100.64.0.7")
	laptop, _, _ := s.Login(ctx, codeB, "", "127.0.0.1")
	_, tabletID, err := s.Authenticate(ctx, tablet)
	if err != nil {
		t.Fatal(err)
	}

	list, err := s.Tokens(ctx, owner, tabletID)
	if err != nil || len(list) != 2 || list[0].Name != "tablet" || !list[0].Current || list[1].Name != "unnamed" || list[1].Current {
		t.Fatalf("tokens = %+v, %v", list, err)
	}
	if list[0].LastUsedAt == nil || list[1].LastUsedAt != nil {
		t.Fatalf("last used = %v, %v", list[0].LastUsedAt, list[1].LastUsedAt)
	}
	first := *list[0].LastUsedAt
	s.now = func() time.Time { return start.Add(30 * time.Second) }
	s.Authenticate(ctx, tablet)
	if list, _ := s.Tokens(ctx, owner, ""); *list[0].LastUsedAt != first {
		t.Fatalf("last used rewritten within a minute: %s", *list[0].LastUsedAt)
	}
	s.now = func() time.Time { return start.Add(2 * time.Minute) }
	s.Authenticate(ctx, tablet)
	if list, _ := s.Tokens(ctx, owner, ""); *list[0].LastUsedAt == first {
		t.Fatal("last used not updated after a minute")
	}

	if err := s.Revoke(ctx, "someone-else", tabletID, "127.0.0.1"); !errors.Is(err, ErrTokenNotFound) {
		t.Fatalf("other owner's revoke: err = %v", err)
	}
	if err := s.Revoke(ctx, owner, tabletID, "127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Authenticate(ctx, tablet); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("revoked token: err = %v", err)
	}
	if _, _, err := s.Authenticate(ctx, laptop); err != nil {
		t.Fatalf("other token after revoke: %v", err)
	}
	if err := s.Revoke(ctx, owner, tabletID, "127.0.0.1"); !errors.Is(err, ErrTokenNotFound) {
		t.Fatalf("second revoke: err = %v", err)
	}

	var events []string
	rows, _ := s.db.Query(`SELECT action || ' ' || remote FROM auth_events ORDER BY id`)
	for rows.Next() {
		var e string
		rows.Scan(&e)
		events = append(events, e)
	}
	rows.Close()
	if want := []string{"login 100.64.0.7", "login 127.0.0.1", "token.revoke 127.0.0.1"}; !slices.Equal(events, want) {
		t.Fatalf("events = %q, want %q", events, want)
	}
}

func TestLoginRejectsBadNames(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	code, _ := s.Bootstrap(ctx, "onat", time.Minute)
	for _, name := range []string{strings.Repeat("x", 65), "tab\nlet"} {
		if _, _, err := s.Login(ctx, code, name, "127.0.0.1"); !errors.Is(err, ErrInvalidName) {
			t.Fatalf("name %q: err = %v", name, err)
		}
	}
	if _, _, err := s.Login(ctx, code, "tablet", "127.0.0.1"); err != nil {
		t.Fatalf("code consumed by rejected names: %v", err)
	}
}
