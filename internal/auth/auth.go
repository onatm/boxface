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

	ErrInvalidIdentity  = errors.New("invalid identity")
	ErrIdentityTaken    = errors.New("identity is bound to another owner")
	ErrIdentityNotFound = errors.New("identity not found")
)

const ProviderTailscale = "tailscale"

type Identity struct {
	ID        string `json:"id"`
	Provider  string `json:"provider"`
	Subject   string `json:"subject"`
	CreatedAt string `json:"created_at"`
}

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

func tokenName(name string) (string, error) {
	if name == "" {
		name = "unnamed"
	}
	if !validName(name) {
		return "", fmt.Errorf("%w: name must be 1 to 64 printable characters", ErrInvalidName)
	}
	return name, nil
}

func (s *Store) Login(ctx context.Context, code, name, remote string) (token, ownerID string, err error) {
	if name, err = tokenName(name); err != nil {
		return "", "", err
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
	if token, err = s.issue(ctx, tx, ownerID, name, remote, ""); err != nil {
		return "", "", err
	}
	return token, ownerID, tx.Commit()
}

func (s *Store) LoginIdentity(ctx context.Context, provider, subject, name, remote string) (token, ownerID string, err error) {
	if name, err = tokenName(name); err != nil {
		return "", "", err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", "", err
	}
	defer tx.Rollback()
	err = tx.QueryRowContext(ctx, `SELECT owner_id FROM identities WHERE provider = ? AND subject = ?`, provider, subject).Scan(&ownerID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", fmt.Errorf("%w: %s login %s is not bound to an owner", ErrUnauthorized, provider, subject)
	}
	if err != nil {
		return "", "", err
	}
	if token, err = s.issue(ctx, tx, ownerID, name, remote, provider+":"+subject); err != nil {
		return "", "", err
	}
	return token, ownerID, tx.Commit()
}

func (s *Store) issue(ctx context.Context, tx *sql.Tx, ownerID, name, remote, identity string) (string, error) {
	b := make([]byte, 32)
	rand.Read(b)
	token := "bfx_" + base64.RawURLEncoding.EncodeToString(b)
	tokenID := id.New()
	if _, err := tx.ExecContext(ctx, `INSERT INTO api_tokens (id, owner_id, hash, name, created_at) VALUES (?, ?, ?, ?, ?)`, tokenID, ownerID, hash(token), name, s.timestamp()); err != nil {
		return "", err
	}
	return token, s.event(ctx, tx, ownerID, "login", tokenID, identity, remote)
}

func (s *Store) event(ctx context.Context, tx *sql.Tx, ownerID, action, tokenID, targetID, remote string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO auth_events (owner_id, action, token_id, target_id, remote, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		ownerID, action, tokenID, targetID, remote, s.timestamp())
	return err
}

func validSubject(subject string) bool {
	if subject == "" || len(subject) > 256 {
		return false
	}
	for _, r := range subject {
		if !unicode.IsPrint(r) || unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

func (s *Store) BindIdentity(ctx context.Context, ownerID, provider, subject, tokenID, remote string) (Identity, bool, error) {
	if provider != ProviderTailscale || !validSubject(subject) {
		return Identity{}, false, fmt.Errorf("%w: %s login %q", ErrInvalidIdentity, provider, subject)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Identity{}, false, err
	}
	defer tx.Rollback()
	ident := Identity{ID: id.New(), Provider: provider, Subject: subject, CreatedAt: s.timestamp()}
	res, err := tx.ExecContext(ctx, `INSERT INTO identities (id, owner_id, provider, subject, created_at) VALUES (?, ?, ?, ?, ?) ON CONFLICT (provider, subject) DO NOTHING`,
		ident.ID, ownerID, provider, subject, ident.CreatedAt)
	if err != nil {
		return Identity{}, false, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		var existingOwner string
		if err := tx.QueryRowContext(ctx, `SELECT id, owner_id, created_at FROM identities WHERE provider = ? AND subject = ?`, provider, subject).
			Scan(&ident.ID, &existingOwner, &ident.CreatedAt); err != nil {
			return Identity{}, false, err
		}
		if existingOwner != ownerID {
			return Identity{}, false, ErrIdentityTaken
		}
		return ident, false, nil
	}
	if err := s.event(ctx, tx, ownerID, "identity.bind", tokenID, provider+":"+subject, remote); err != nil {
		return Identity{}, false, err
	}
	return ident, true, tx.Commit()
}

func (s *Store) BindOwnerIdentity(ctx context.Context, ownerName, provider, subject string) (Identity, error) {
	var ownerID string
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM owners WHERE name = ?`, ownerName).Scan(&ownerID); err != nil {
		return Identity{}, err
	}
	ident, _, err := s.BindIdentity(ctx, ownerID, provider, subject, "", "host")
	return ident, err
}

func (s *Store) Identities(ctx context.Context, ownerID string) ([]Identity, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, provider, subject, created_at FROM identities WHERE owner_id = ? ORDER BY created_at, rowid`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []Identity{}
	for rows.Next() {
		var i Identity
		if err := rows.Scan(&i.ID, &i.Provider, &i.Subject, &i.CreatedAt); err != nil {
			return nil, err
		}
		list = append(list, i)
	}
	return list, rows.Err()
}

func (s *Store) UnbindIdentity(ctx context.Context, ownerID, identityID, tokenID, remote string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var provider, subject string
	err = tx.QueryRowContext(ctx, `DELETE FROM identities WHERE id = ? AND owner_id = ? RETURNING provider, subject`, identityID, ownerID).Scan(&provider, &subject)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrIdentityNotFound
	}
	if err != nil {
		return err
	}
	if err := s.event(ctx, tx, ownerID, "identity.unbind", tokenID, provider+":"+subject, remote); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) Record(ctx context.Context, ownerID, action, tokenID, targetID, remote string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO auth_events (owner_id, action, token_id, target_id, remote, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		ownerID, action, tokenID, targetID, remote, s.timestamp())
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
	if err := s.event(ctx, tx, ownerID, "token.revoke", tokenID, "", remote); err != nil {
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
