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
	"fmt"
	"time"
	"unicode"

	"github.com/onatm/boxface/internal/id"
)

var (
	ErrUnauthorized  = errors.New("unauthorized")
	ErrInvalidName   = errors.New("invalid token name")
	ErrTokenNotFound = errors.New("token not found")
)

const lastUsedResolution = time.Minute

type Token struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	CreatedAt  string  `json:"created_at"`
	LastUsedAt *string `json:"last_used_at"`
	Current    bool    `json:"current"`
}

func validName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for _, r := range name {
		if !unicode.IsPrint(r) {
			return false
		}
	}
	return true
}

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

func (s *Store) Login(ctx context.Context, code, name, remote string) (token, ownerID string, err error) {
	if name == "" {
		name = "unnamed"
	}
	if !validName(name) {
		return "", "", fmt.Errorf("%w: name must be 1 to 64 printable characters", ErrInvalidName)
	}
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
	tokenID := id.New()
	if _, err := tx.ExecContext(ctx, `INSERT INTO api_tokens (id, owner_id, hash, name, created_at) VALUES (?, ?, ?, ?, ?)`, tokenID, ownerID, hash(token), name, now); err != nil {
		return "", "", err
	}
	if err := s.event(ctx, tx, ownerID, "login", tokenID, remote); err != nil {
		return "", "", err
	}
	return token, ownerID, tx.Commit()
}

func (s *Store) event(ctx context.Context, tx *sql.Tx, ownerID, action, tokenID, remote string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO auth_events (owner_id, action, token_id, remote, created_at) VALUES (?, ?, ?, ?, ?)`,
		ownerID, action, tokenID, remote, s.timestamp())
	return err
}

func (s *Store) Authenticate(ctx context.Context, token string) (ownerID, tokenID string, err error) {
	var lastUsed sql.NullString
	err = s.db.QueryRowContext(ctx, `SELECT owner_id, id, last_used_at FROM api_tokens WHERE hash = ? AND revoked_at IS NULL`, hash(token)).
		Scan(&ownerID, &tokenID, &lastUsed)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", ErrUnauthorized
	}
	if err != nil {
		return "", "", err
	}
	if t, perr := time.Parse(time.RFC3339Nano, lastUsed.String); !lastUsed.Valid || perr != nil || s.now().Sub(t) >= lastUsedResolution {
		if _, err := s.db.ExecContext(ctx, `UPDATE api_tokens SET last_used_at = ? WHERE id = ?`, s.timestamp(), tokenID); err != nil {
			return "", "", err
		}
	}
	return ownerID, tokenID, nil
}

func (s *Store) Tokens(ctx context.Context, ownerID, currentID string) ([]Token, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, created_at, last_used_at FROM api_tokens WHERE owner_id = ? AND revoked_at IS NULL ORDER BY created_at, rowid`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []Token{}
	for rows.Next() {
		var t Token
		var lastUsed sql.NullString
		if err := rows.Scan(&t.ID, &t.Name, &t.CreatedAt, &lastUsed); err != nil {
			return nil, err
		}
		if lastUsed.Valid {
			t.LastUsedAt = &lastUsed.String
		}
		t.Current = t.ID == currentID
		list = append(list, t)
	}
	return list, rows.Err()
}

func (s *Store) Revoke(ctx context.Context, ownerID, tokenID, remote string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE api_tokens SET revoked_at = ? WHERE id = ? AND owner_id = ? AND revoked_at IS NULL`, s.timestamp(), tokenID, ownerID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrTokenNotFound
	}
	if err := s.event(ctx, tx, ownerID, "token.revoke", tokenID, remote); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) RevokeAll(ctx context.Context, ownerName string) (int64, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE api_tokens SET revoked_at = ? WHERE revoked_at IS NULL AND owner_id = (SELECT id FROM owners WHERE name = ?)`,
		s.timestamp(), ownerName)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
