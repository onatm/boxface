package sshkeys

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/onatm/boxface/internal/id"
)

var (
	ErrInvalid  = errors.New("invalid SSH key")
	ErrNotFound = errors.New("SSH key not found")
)

var keyTypes = map[string]bool{
	"ssh-ed25519":                        true,
	"ssh-rsa":                            true,
	"ecdsa-sha2-nistp256":                true,
	"ecdsa-sha2-nistp384":                true,
	"ecdsa-sha2-nistp521":                true,
	"sk-ssh-ed25519@openssh.com":         true,
	"sk-ecdsa-sha2-nistp256@openssh.com": true,
}

type Key struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	PublicKey   string `json:"public_key"`
	Fingerprint string `json:"fingerprint"`
	CreatedAt   string `json:"created_at"`
}

func Parse(line string) (publicKey, fingerprint string, err error) {
	line = strings.TrimSpace(line)
	if len(line) > 16<<10 || strings.ContainsAny(line, "\r\n") {
		return "", "", fmt.Errorf("%w: expected one public key line", ErrInvalid)
	}
	fields := strings.Fields(line)
	if len(fields) < 2 || !keyTypes[fields[0]] {
		return "", "", fmt.Errorf("%w: expected \"<type> <base64> [comment]\" with a supported key type", ErrInvalid)
	}
	blob, err := base64.StdEncoding.DecodeString(fields[1])
	if err != nil || len(blob) < 4 {
		return "", "", fmt.Errorf("%w: key data is not valid base64", ErrInvalid)
	}
	n := binary.BigEndian.Uint32(blob)
	if uint64(n)+4 > uint64(len(blob)) || !bytes.Equal(blob[4:4+n], []byte(fields[0])) {
		return "", "", fmt.Errorf("%w: key data does not match type %s", ErrInvalid, fields[0])
	}
	sum := sha256.Sum256(blob)
	return fields[0] + " " + fields[1], "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:]), nil
}

type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
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

func (s *Store) Add(ctx context.Context, ownerID, name, line string) (Key, bool, error) {
	if !validName(name) {
		return Key{}, false, fmt.Errorf("%w: name must be 1 to 64 printable characters", ErrInvalid)
	}
	publicKey, fingerprint, err := Parse(line)
	if err != nil {
		return Key{}, false, err
	}
	k := Key{ID: id.New(), Name: name, PublicKey: publicKey, Fingerprint: fingerprint, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	res, err := s.db.ExecContext(ctx, `INSERT INTO ssh_keys (id, owner_id, name, public_key, fingerprint, created_at) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (owner_id, fingerprint) DO NOTHING`, k.ID, ownerID, k.Name, k.PublicKey, k.Fingerprint, k.CreatedAt)
	if err != nil {
		return Key{}, false, err
	}
	if n, _ := res.RowsAffected(); n == 1 {
		return k, true, nil
	}
	err = s.db.QueryRowContext(ctx, `SELECT id, name, public_key, created_at FROM ssh_keys WHERE owner_id = ? AND fingerprint = ?`, ownerID, fingerprint).
		Scan(&k.ID, &k.Name, &k.PublicKey, &k.CreatedAt)
	return k, false, err
}

func (s *Store) List(ctx context.Context, ownerID string) ([]Key, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, public_key, fingerprint, created_at FROM ssh_keys WHERE owner_id = ? ORDER BY created_at, rowid`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []Key{}
	for rows.Next() {
		var k Key
		if err := rows.Scan(&k.ID, &k.Name, &k.PublicKey, &k.Fingerprint, &k.CreatedAt); err != nil {
			return nil, err
		}
		list = append(list, k)
	}
	return list, rows.Err()
}

func (s *Store) Delete(ctx context.Context, ownerID, keyID string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM ssh_keys WHERE id = ? AND owner_id = ?`, keyID, ownerID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrNotFound
	}
	return nil
}
