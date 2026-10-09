package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"time"

	"github.com/onatm/boxface/internal/id"
)

var ErrUnauthorized = errors.New("unauthorized")

type Store struct {
	db  *sql.DB
	now func() time.Time
}

func New(db *sql.DB) *Store {
	return &Store{db: db, now: time.Now}
}

func hash(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

func (s *Store) timestamp() string { return s.now().UTC().Format(time.RFC3339Nano) }

func (s *Store) Bootstrap(ctx context.Context, ownerName string, ttl time.Duration) (string, error) {
	if ownerName == "" {
		return "", errors.New("owner name is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var ownerID string
	err = tx.QueryRowContext(ctx, `SELECT id FROM owners WHERE name = ?`, ownerName).Scan(&ownerID)
	if errors.Is(err, sql.ErrNoRows) {
		ownerID = id.New()
		if _, err := tx.ExecContext(ctx, `INSERT INTO owners (id, name, created_at) VALUES (?, ?, ?)`, ownerID, ownerName, s.timestamp()); err != nil {
			return "", err
		}
	} else if err != nil {
		return "", err
	}

	b := make([]byte, 20)
	rand.Read(b)
	code := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b)
	expires := s.now().Add(ttl).UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT INTO bootstrap_codes (hash, owner_id, expires_at) VALUES (?, ?, ?)`, hash(code), ownerID, expires); err != nil {
		return "", err
	}
	return code, tx.Commit()
}

func (s *Store) Login(ctx context.Context, code string) (token, ownerID string, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", "", err
	}
	defer tx.Rollback()
	now := s.timestamp()
	res, err := tx.ExecContext(ctx, `UPDATE bootstrap_codes SET used_at = ? WHERE hash = ? AND used_at IS NULL AND expires_at > ?`, now, hash(code), now)
	if err != nil {
		return "", "", err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return "", "", ErrUnauthorized
	}
	if err := tx.QueryRowContext(ctx, `SELECT owner_id FROM bootstrap_codes WHERE hash = ?`, hash(code)).Scan(&ownerID); err != nil {
		return "", "", err
	}
	b := make([]byte, 32)
	rand.Read(b)
	token = "bfx_" + base64.RawURLEncoding.EncodeToString(b)
	if _, err := tx.ExecContext(ctx, `INSERT INTO api_tokens (id, owner_id, hash, created_at) VALUES (?, ?, ?, ?)`, id.New(), ownerID, hash(token), now); err != nil {
		return "", "", err
	}
	return token, ownerID, tx.Commit()
}

func (s *Store) Authenticate(ctx context.Context, token string) (string, error) {
	var ownerID string
	err := s.db.QueryRowContext(ctx, `SELECT owner_id FROM api_tokens WHERE hash = ? AND revoked_at IS NULL`, hash(token)).Scan(&ownerID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrUnauthorized
	}
	return ownerID, err
}

func (s *Store) RevokeAll(ctx context.Context, ownerName string) (int64, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE api_tokens SET revoked_at = ? WHERE revoked_at IS NULL AND owner_id = (SELECT id FROM owners WHERE name = ?)`,
		s.timestamp(), ownerName)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
