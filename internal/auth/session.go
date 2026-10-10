package auth

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"time"

	"github.com/onatm/boxface/internal/id"
)

const SessionTTL = 30 * 24 * time.Hour

func (s *Store) WebLogin(ctx context.Context, code, remote string) (secret, ownerID string, err error) {
	return s.webLogin(ctx, remote, "", func(tx *sql.Tx) (string, error) { return s.redeemCode(ctx, tx, code) })
}

func (s *Store) WebLoginIdentity(ctx context.Context, provider, subject, remote string) (secret, ownerID string, err error) {
	return s.webLogin(ctx, remote, provider+":"+subject, func(tx *sql.Tx) (string, error) { return identityOwner(ctx, tx, provider, subject) })
}

func (s *Store) webLogin(ctx context.Context, remote, identity string, owner func(tx *sql.Tx) (string, error)) (secret, ownerID string, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", "", err
	}
	defer tx.Rollback()
	if ownerID, err = owner(tx); err != nil {
		return "", "", err
	}
	b := make([]byte, 32)
	rand.Read(b)
	secret = "bfs_" + base64.RawURLEncoding.EncodeToString(b)
	sessionID := id.New()
	if _, err := tx.ExecContext(ctx, `INSERT INTO web_sessions (id, owner_id, hash, created_at, expires_at) VALUES (?, ?, ?, ?, ?)`,
		sessionID, ownerID, hash(secret), s.timestamp(), s.now().Add(SessionTTL).UTC().Format(time.RFC3339Nano)); err != nil {
		return "", "", err
	}
	if err := s.event(ctx, tx, ownerID, "web.login", "web:"+sessionID, identity, remote); err != nil {
		return "", "", err
	}
	return secret, ownerID, tx.Commit()
}

func (s *Store) AuthenticateSession(ctx context.Context, secret string) (ownerID, sessionID string, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT owner_id, id FROM web_sessions WHERE hash = ? AND ended_at IS NULL AND expires_at > ?`, hash(secret), s.timestamp()).
		Scan(&ownerID, &sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", ErrUnauthorized
	}
	return ownerID, "web:" + sessionID, err
}

func (s *Store) EndSession(ctx context.Context, secret string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE web_sessions SET ended_at = ? WHERE hash = ? AND ended_at IS NULL`, s.timestamp(), hash(secret))
	return err
}

func (s *Store) OwnerName(ctx context.Context, ownerID string) (string, error) {
	var name string
	err := s.db.QueryRowContext(ctx, `SELECT name FROM owners WHERE id = ?`, ownerID).Scan(&name)
	return name, err
}

func (s *Store) IdentityBound(ctx context.Context, provider, subject string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM identities WHERE provider = ? AND subject = ?`, provider, subject).Scan(&n)
	return n > 0, err
}
