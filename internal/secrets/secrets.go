package secrets

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/onatm/boxface/internal/id"
	"github.com/onatm/boxface/internal/lifecycle"
	gosecrets "gocloud.dev/secrets"
	"gocloud.dev/secrets/localsecrets"
)

var (
	ErrInvalid  = errors.New("invalid secret")
	ErrNotFound = errors.New("secret not found")
	ErrConflict = errors.New("secret conflict")
	ErrKey      = errors.New("secret key does not decrypt stored secrets")
)

const maxValue = 8 << 10

var Consumers = []string{"claude-code", "codex", "opencode", "pi"}

var (
	namePattern     = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
	variablePattern = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,127}$`)
	reserved        = []string{"PATH", "HOME", "USER", "LOGNAME", "SHELL", "PWD", "TERM", "TMPDIR", "IFS", "ENV", "BASH_ENV"}
)

type Binding struct {
	Consumer string `json:"consumer"`
	Variable string `json:"variable"`
}

type Secret struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Bindings  []Binding `json:"bindings"`
	CreatedAt string    `json:"created_at"`
	UpdatedAt string    `json:"updated_at"`
}

type Store struct {
	db     *sql.DB
	keeper *gosecrets.Keeper
}

func NewStore(db *sql.DB, keeper *gosecrets.Keeper) *Store {
	return &Store{db: db, keeper: keeper}
}

func OpenKeeper(ctx context.Context, url string) (*gosecrets.Keeper, error) {
	if path, ok := strings.CutPrefix(url, "file://"); ok {
		key, err := loadOrCreateKey(path)
		if err != nil {
			return nil, fmt.Errorf("secrets key %s: %w", path, err)
		}
		return localsecrets.NewKeeper(key), nil
	}
	return gosecrets.OpenKeeper(ctx, url)
}

func loadOrCreateKey(path string) ([32]byte, error) {
	var key [32]byte
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		rand.Read(key[:])
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return key, err
		}
		if _, err := f.Write(key[:]); err != nil {
			f.Close()
			return key, err
		}
		return key, f.Close()
	}
	if err != nil {
		return key, err
	}
	if len(b) != len(key) {
		return key, fmt.Errorf("want %d bytes, found %d", len(key), len(b))
	}
	copy(key[:], b)
	return key, nil
}

func timestamp() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func validValue(value string) error {
	if value == "" || len(value) > maxValue || strings.ContainsRune(value, 0) {
		return fmt.Errorf("%w: value must be 1 to %d bytes with no NUL", ErrInvalid, maxValue)
	}
	return nil
}

func validBindings(bindings []Binding) error {
	seen := map[Binding]bool{}
	for _, b := range bindings {
		if !slices.Contains(Consumers, b.Consumer) {
			return fmt.Errorf("%w: consumer %q must be one of %s", ErrInvalid, b.Consumer, strings.Join(Consumers, ", "))
		}
		if !variablePattern.MatchString(b.Variable) || slices.Contains(reserved, b.Variable) || strings.HasPrefix(b.Variable, "LD_") {
			return fmt.Errorf("%w: variable %q is not allowed", ErrInvalid, b.Variable)
		}
		if seen[b] {
			return fmt.Errorf("%w: %s is bound twice for %s", ErrInvalid, b.Variable, b.Consumer)
		}
		seen[b] = true
	}
	return nil
}

func (s *Store) Create(ctx context.Context, ownerID, name, value string, bindings []Binding) (Secret, error) {
	if !namePattern.MatchString(name) {
		return Secret{}, fmt.Errorf("%w: name must match %s", ErrInvalid, namePattern)
	}
	if err := validValue(value); err != nil {
		return Secret{}, err
	}
	if err := validBindings(bindings); err != nil {
		return Secret{}, err
	}
	ciphertext, err := s.keeper.Encrypt(ctx, []byte(value))
	if err != nil {
		return Secret{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Secret{}, err
	}
	defer tx.Rollback()
	now := timestamp()
	sec := Secret{ID: id.New(), Name: name, Bindings: bindings, CreatedAt: now, UpdatedAt: now}
	if _, err := tx.ExecContext(ctx, `INSERT INTO secrets (id, owner_id, name, ciphertext, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		sec.ID, ownerID, name, ciphertext, now, now); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return Secret{}, fmt.Errorf("%w: a secret named %s exists", ErrConflict, name)
		}
		return Secret{}, err
	}
	if err := bind(ctx, tx, ownerID, sec.ID, bindings); err != nil {
		return Secret{}, err
	}
	return sec, tx.Commit()
}

func bind(ctx context.Context, tx *sql.Tx, ownerID, secretID string, bindings []Binding) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM secret_bindings WHERE secret_id = ?`, secretID); err != nil {
		return err
	}
	for _, b := range bindings {
		if _, err := tx.ExecContext(ctx, `INSERT INTO secret_bindings (secret_id, owner_id, consumer, variable) VALUES (?, ?, ?, ?)`,
			secretID, ownerID, b.Consumer, b.Variable); err != nil {
			if strings.Contains(err.Error(), "UNIQUE") {
				return fmt.Errorf("%w: %s for %s is bound by another secret", ErrConflict, b.Variable, b.Consumer)
			}
			return err
		}
	}
	return nil
}

func (s *Store) Update(ctx context.Context, ownerID, secretID string, value *string, bindings *[]Binding) (Secret, error) {
	var ciphertext []byte
	if value != nil {
		if err := validValue(*value); err != nil {
			return Secret{}, err
		}
		var err error
		if ciphertext, err = s.keeper.Encrypt(ctx, []byte(*value)); err != nil {
			return Secret{}, err
		}
	}
	if bindings != nil {
		if err := validBindings(*bindings); err != nil {
			return Secret{}, err
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Secret{}, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE secrets SET updated_at = ?, ciphertext = coalesce(?, ciphertext) WHERE id = ? AND owner_id = ?`,
		timestamp(), ciphertext, secretID, ownerID)
	if err != nil {
		return Secret{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return Secret{}, ErrNotFound
	}
	if bindings != nil {
		if err := bind(ctx, tx, ownerID, secretID, *bindings); err != nil {
			return Secret{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Secret{}, err
	}
	return s.get(ctx, ownerID, secretID)
}

func (s *Store) Delete(ctx context.Context, ownerID, secretID string) (string, error) {
	var name string
	err := s.db.QueryRowContext(ctx, `DELETE FROM secrets WHERE id = ? AND owner_id = ? RETURNING name`, secretID, ownerID).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return name, err
}

func (s *Store) get(ctx context.Context, ownerID, secretID string) (Secret, error) {
	list, err := s.list(ctx, ownerID, secretID)
	if err != nil {
		return Secret{}, err
	}
	if len(list) == 0 {
		return Secret{}, ErrNotFound
	}
	return list[0], nil
}

func (s *Store) List(ctx context.Context, ownerID string) ([]Secret, error) {
	return s.list(ctx, ownerID, "")
}

func (s *Store) list(ctx context.Context, ownerID, secretID string) ([]Secret, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, created_at, updated_at FROM secrets WHERE owner_id = ? AND (? = '' OR id = ?) ORDER BY name`,
		ownerID, secretID, secretID)
	if err != nil {
		return nil, err
	}
	list := []Secret{}
	index := map[string]int{}
	for rows.Next() {
		sec := Secret{Bindings: []Binding{}}
		if err := rows.Scan(&sec.ID, &sec.Name, &sec.CreatedAt, &sec.UpdatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		index[sec.ID] = len(list)
		list = append(list, sec)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	brows, err := s.db.QueryContext(ctx, `SELECT secret_id, consumer, variable FROM secret_bindings WHERE owner_id = ? ORDER BY consumer, variable`, ownerID)
	if err != nil {
		return nil, err
	}
	defer brows.Close()
	for brows.Next() {
		var secID string
		var b Binding
		if err := brows.Scan(&secID, &b.Consumer, &b.Variable); err != nil {
			return nil, err
		}
		if i, ok := index[secID]; ok {
			list[i].Bindings = append(list[i].Bindings, b)
		}
	}
	return list, brows.Err()
}

func Rekey(ctx context.Context, db *sql.DB, from, to *gosecrets.Keeper) (int, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id, name, ciphertext FROM secrets`)
	if err != nil {
		return 0, err
	}
	type entry struct {
		id, name   string
		ciphertext []byte
	}
	var all []entry
	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.id, &e.name, &e.ciphertext); err != nil {
			rows.Close()
			return 0, err
		}
		all = append(all, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, e := range all {
		plain, err := from.Decrypt(ctx, e.ciphertext)
		if err != nil {
			return 0, fmt.Errorf("%w: %s", ErrKey, e.name)
		}
		ciphertext, err := to.Encrypt(ctx, plain)
		if err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE secrets SET ciphertext = ? WHERE id = ?`, ciphertext, e.id); err != nil {
			return 0, err
		}
	}
	return len(all), tx.Commit()
}

type Resolved struct {
	Env     lifecycle.Credentials
	Secrets []string
}

func (s *Store) Resolve(ctx context.Context, ownerID string) (Resolved, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT s.name, s.ciphertext, b.consumer, b.variable FROM secret_bindings b JOIN secrets s ON s.id = b.secret_id
		WHERE b.owner_id = ? ORDER BY s.name, b.consumer, b.variable`, ownerID)
	if err != nil {
		return Resolved{}, err
	}
	defer rows.Close()
	r := Resolved{Env: lifecycle.Credentials{}}
	plain := map[string]string{}
	for rows.Next() {
		var name, consumer, variable string
		var ciphertext []byte
		if err := rows.Scan(&name, &ciphertext, &consumer, &variable); err != nil {
			return Resolved{}, err
		}
		value, ok := plain[name]
		if !ok {
			b, err := s.keeper.Decrypt(ctx, ciphertext)
			if err != nil {
				return Resolved{}, fmt.Errorf("%w: %s", ErrKey, name)
			}
			value = string(b)
			plain[name] = value
			r.Secrets = append(r.Secrets, name)
		}
		if r.Env[consumer] == nil {
			r.Env[consumer] = map[string]string{}
		}
		r.Env[consumer][variable] = value
	}
	return r, rows.Err()
}
