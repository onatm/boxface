package profiles

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/onatm/boxface/internal/id"
)

var (
	ErrNotFound  = errors.New("profile not found")
	ErrNameTaken = errors.New("profile name already in use")
	ErrNotReady  = errors.New("profile has no ready revision")
	ErrInUse     = errors.New("profile is in use")
	namePattern  = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
)

type Revision struct {
	ID             string `json:"id"`
	Number         int    `json:"number"`
	Spec           Spec   `json:"spec"`
	BuildKey       string `json:"build_key"`
	State          string `json:"state"`
	Error          string `json:"error,omitempty"`
	LogTail        string `json:"log_tail,omitempty"`
	DotfilesCommit string `json:"dotfiles_commit,omitempty"`
	Pruned         bool   `json:"pruned,omitempty"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
}

type Profile struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	CreatedAt string     `json:"created_at"`
	Latest    *Revision  `json:"latest,omitempty"`
	Revisions []Revision `json:"revisions,omitempty"`
}

type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

func timestamp() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func (s *Store) Create(ctx context.Context, ownerID, name string, spec Spec) (Profile, error) {
	if !namePattern.MatchString(name) {
		return Profile{}, fmt.Errorf("%w: name must match %s", ErrInvalid, namePattern)
	}
	spec, err := spec.Normalize()
	if err != nil {
		return Profile{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Profile{}, err
	}
	defer tx.Rollback()
	p := Profile{ID: id.New(), Name: name, CreatedAt: timestamp()}
	if _, err := tx.ExecContext(ctx, `INSERT INTO profiles (id, owner_id, name, created_at) VALUES (?, ?, ?, ?)`, p.ID, ownerID, name, p.CreatedAt); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return Profile{}, fmt.Errorf("%w: %s", ErrNameTaken, name)
		}
		return Profile{}, err
	}
	rev, err := addRevision(ctx, tx, ownerID, p.ID, spec)
	if err != nil {
		return Profile{}, err
	}
	p.Latest = &rev
	return p, tx.Commit()
}

func (s *Store) AddRevision(ctx context.Context, ownerID, profileID string, spec Spec) (Revision, error) {
	spec, err := spec.Normalize()
	if err != nil {
		return Revision{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Revision{}, err
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM profiles WHERE id = ? AND owner_id = ? AND deleted_at IS NULL`, profileID, ownerID).Scan(&exists); err != nil {
		return Revision{}, err
	}
	if exists == 0 {
		return Revision{}, ErrNotFound
	}
	rev, err := addRevision(ctx, tx, ownerID, profileID, spec)
	if err != nil {
		return Revision{}, err
	}
	return rev, tx.Commit()
}

func addRevision(ctx context.Context, tx *sql.Tx, ownerID, profileID string, spec Spec) (Revision, error) {
	var number int
	if err := tx.QueryRowContext(ctx, `SELECT coalesce(max(number), 0) + 1 FROM profile_revisions WHERE profile_id = ?`, profileID).Scan(&number); err != nil {
		return Revision{}, err
	}
	encoded, err := json.Marshal(spec)
	if err != nil {
		return Revision{}, err
	}
	now := timestamp()
	rev := Revision{ID: id.New(), Number: number, Spec: spec, BuildKey: BuildKey(ownerID, spec), State: "pending", CreatedAt: now, UpdatedAt: now}
	_, err = tx.ExecContext(ctx, `INSERT INTO profile_revisions (id, profile_id, number, spec, build_key, state, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		rev.ID, profileID, number, string(encoded), rev.BuildKey, rev.State, now, now)
	return rev, err
}

const revisionColumns = `r.id, r.number, r.spec, r.build_key, r.state, r.error, r.log_tail, r.dotfiles_commit, r.pruned_at IS NOT NULL, r.created_at, r.updated_at`

func scanRevision(row interface{ Scan(...any) error }) (Revision, error) {
	var r Revision
	var spec string
	if err := row.Scan(&r.ID, &r.Number, &spec, &r.BuildKey, &r.State, &r.Error, &r.LogTail, &r.DotfilesCommit, &r.Pruned, &r.CreatedAt, &r.UpdatedAt); err != nil {
		return Revision{}, err
	}
	return r, json.Unmarshal([]byte(spec), &r.Spec)
}

func (s *Store) List(ctx context.Context, ownerID string) ([]Profile, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, created_at FROM profiles WHERE owner_id = ? AND deleted_at IS NULL ORDER BY name`, ownerID)
	if err != nil {
		return nil, err
	}
	list := []Profile{}
	for rows.Next() {
		var p Profile
		if err := rows.Scan(&p.ID, &p.Name, &p.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		list = append(list, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range list {
		rev, err := scanRevision(s.db.QueryRowContext(ctx, `SELECT `+revisionColumns+` FROM profile_revisions r WHERE r.profile_id = ? ORDER BY r.number DESC LIMIT 1`, list[i].ID))
		if err != nil {
			return nil, err
		}
		list[i].Latest = &rev
	}
	return list, nil
}

func (s *Store) Get(ctx context.Context, ownerID, profileID string) (Profile, error) {
	var p Profile
	err := s.db.QueryRowContext(ctx, `SELECT id, name, created_at FROM profiles WHERE id = ? AND owner_id = ? AND deleted_at IS NULL`, profileID, ownerID).Scan(&p.ID, &p.Name, &p.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Profile{}, ErrNotFound
	}
	if err != nil {
		return Profile{}, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+revisionColumns+` FROM profile_revisions r WHERE r.profile_id = ? ORDER BY r.number DESC`, profileID)
	if err != nil {
		return Profile{}, err
	}
	defer rows.Close()
	for rows.Next() {
		rev, err := scanRevision(rows)
		if err != nil {
			return Profile{}, err
		}
		p.Revisions = append(p.Revisions, rev)
	}
	if len(p.Revisions) > 0 {
		p.Latest = &p.Revisions[0]
	}
	return p, rows.Err()
}

func (s *Store) Ready(ctx context.Context, ownerID, ref string) (Revision, error) {
	rev, err := scanRevision(s.db.QueryRowContext(ctx, `SELECT `+revisionColumns+` FROM profile_revisions r JOIN profiles p ON p.id = r.profile_id
		WHERE p.owner_id = ? AND (p.id = ? OR p.name = ?) AND p.deleted_at IS NULL AND r.state = 'ready' ORDER BY r.number DESC LIMIT 1`, ownerID, ref, ref))
	if errors.Is(err, sql.ErrNoRows) {
		var n int
		s.db.QueryRowContext(ctx, `SELECT count(*) FROM profiles WHERE owner_id = ? AND (id = ? OR name = ?) AND deleted_at IS NULL`, ownerID, ref, ref).Scan(&n)
		if n == 0 {
			return Revision{}, fmt.Errorf("%w: %s", ErrNotFound, ref)
		}
		return Revision{}, fmt.Errorf("%w: %s", ErrNotReady, ref)
	}
	return rev, err
}

func (s *Store) Delete(ctx context.Context, ownerID, profileID, defaultProfile string) (string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var name string
	err = tx.QueryRowContext(ctx, `SELECT name FROM profiles WHERE id = ? AND owner_id = ? AND deleted_at IS NULL`, profileID, ownerID).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if name == defaultProfile {
		return "", fmt.Errorf("%w: %s is the default profile; choose another default first", ErrInUse, name)
	}
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT e.name FROM environments e JOIN profile_revisions r ON r.id = e.profile_revision_id
		WHERE r.profile_id = ? AND e.observed_state <> 'deleted' ORDER BY e.name`, profileID)
	if err != nil {
		return "", err
	}
	var users []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return "", err
		}
		users = append(users, n)
	}
	rows.Close()
	if len(users) > 0 {
		return "", fmt.Errorf("%w: environments %s use it", ErrInUse, strings.Join(users, ", "))
	}
	if _, err := tx.ExecContext(ctx, `UPDATE profiles SET deleted_at = ?, name = ? WHERE id = ?`, timestamp(), "deleted-"+profileID, profileID); err != nil {
		return "", err
	}
	return name, tx.Commit()
}
