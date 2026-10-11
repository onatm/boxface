package lifecycle

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/onatm/boxface/internal/id"
)

const (
	TypeCreate = "environment.create"
	TypeStart  = "environment.start"
	TypeStop   = "environment.stop"
	TypeDelete = "environment.delete"
)

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

type CreateRequest struct {
	Name      string `json:"name"`
	ImageID   string `json:"image_id"`
	VCPUs     int    `json:"vcpus"`
	MemoryMiB int    `json:"memory_mib"`
	DiskGiB   int    `json:"disk_gib"`
	Profile   string `json:"profile,omitempty"`
}

type Accepted struct {
	OperationID   string `json:"operation_id"`
	EnvironmentID string `json:"environment_id"`
}

type Operation struct {
	ID             string   `json:"id"`
	Type           string   `json:"type"`
	TargetID       string   `json:"target_id"`
	State          string   `json:"state"`
	ErrorCode      string   `json:"error_code,omitempty"`
	ErrorMessage   string   `json:"error_message,omitempty"`
	CompletedSteps []string `json:"completed_steps"`
}

type Environment struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	ImageID       string `json:"image_id"`
	VCPUs         int    `json:"vcpus"`
	MemoryMiB     int    `json:"memory_mib"`
	DesiredState  string `json:"desired_state"`
	ObservedState string `json:"observed_state"`
	Error         string `json:"error,omitempty"`
	DiskID        string `json:"disk_id"`
	DiskRetention string `json:"disk_retention"`
	Profile       string `json:"profile,omitempty"`
	ProfileNumber int    `json:"profile_revision,omitempty"`
	ProfileLatest int    `json:"profile_latest_ready,omitempty"`
}

type Defaults struct {
	Image     string `json:"image"`
	VCPUs     int    `json:"vcpus"`
	MemoryMiB int    `json:"memory_mib"`
	DiskGiB   int    `json:"disk_gib"`
	Profile   string `json:"profile"`
}

var builtinDefaults = Defaults{Image: "main", VCPUs: 2, MemoryMiB: 2048, DiskGiB: 20, Profile: "main"}

type createParams struct {
	CreateRequest
	AuthorizedKeys []string `json:"authorized_keys"`
}

type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

type targetParams struct {
	EnvironmentID  string   `json:"environment_id"`
	Purge          bool     `json:"purge,omitempty"`
	AuthorizedKeys []string `json:"authorized_keys,omitempty"`
}

type Capacity struct {
	Environments int
	VCPUs        int
	MemoryMiB    int
	DiskGiB      int
}

type Service struct {
	db       *sql.DB
	runner   *Runner
	capacity Capacity
}

func NewService(db *sql.DB, runner *Runner, capacity Capacity) *Service {
	return &Service{db: db, runner: runner, capacity: capacity}
}

func (s *Service) Create(ctx context.Context, ownerID, idempotencyKey string, req CreateRequest) (Accepted, error) {
	if !namePattern.MatchString(req.Name) {
		return Accepted{}, fmt.Errorf("%w: name must match %s", ErrInvalid, namePattern)
	}
	return s.submit(ctx, ownerID, idempotencyKey, TypeCreate, req, func(tx *sql.Tx, opID string) (string, any, error) {
		d, err := defaults(ctx, tx, ownerID)
		if err != nil {
			return "", nil, err
		}
		resolved := req
		for _, f := range []struct {
			value *int
			def   int
		}{{&resolved.VCPUs, d.VCPUs}, {&resolved.MemoryMiB, d.MemoryMiB}, {&resolved.DiskGiB, d.DiskGiB}} {
			if *f.value == 0 {
				*f.value = f.def
			}
		}
		if resolved.VCPUs < 1 || resolved.MemoryMiB < 128 || resolved.DiskGiB < 1 {
			return "", nil, fmt.Errorf("%w: vcpus must be at least 1, memory_mib at least 128, disk_gib at least 1", ErrInvalid)
		}
		ref := resolved.ImageID
		if ref == "" {
			ref = d.Image
		}
		if resolved.ImageID, err = resolveImage(ctx, tx, ref); err != nil {
			return "", nil, err
		}
		profileRef, explicit := resolved.Profile, resolved.Profile != ""
		if !explicit {
			profileRef = d.Profile
		}
		revisionID, err := resolveProfile(ctx, tx, ownerID, profileRef, explicit)
		if err != nil {
			return "", nil, err
		}
		keys, err := authorizedKeys(ctx, tx, ownerID)
		if err != nil {
			return "", nil, err
		}
		var taken int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM environments WHERE owner_id = ? AND name = ? AND observed_state <> 'deleted'`, ownerID, resolved.Name).Scan(&taken); err != nil {
			return "", nil, err
		}
		if taken > 0 {
			return "", nil, ErrNameTaken
		}
		if err := s.checkCapacity(ctx, tx, resolved); err != nil {
			return "", nil, err
		}
		envID, diskID, now := id.New(), id.New(), timestamp()
		if _, err := tx.ExecContext(ctx, `INSERT INTO environments (id, owner_id, name, image_id, vcpus, memory_mib, desired_state, observed_state, created_at, updated_at, profile_revision_id)
			VALUES (?, ?, ?, ?, ?, ?, 'running', 'pending', ?, ?, ?)`, envID, ownerID, resolved.Name, resolved.ImageID, resolved.VCPUs, resolved.MemoryMiB, now, now, revisionID); err != nil {
			return "", nil, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO data_disks (id, owner_id, environment_id, size_gib, retention, created_at, updated_at)
			VALUES (?, ?, ?, ?, 'attached', ?, ?)`, diskID, ownerID, envID, resolved.DiskGiB, now, now); err != nil {
			return "", nil, err
		}
		return envID, createParams{resolved, keys}, audit(ctx, tx, ownerID, "environment.create", envID, opID)
	})
}

func resolveImage(ctx context.Context, q queryer, ref string) (string, error) {
	var imageID, status string
	err := q.QueryRowContext(ctx, `SELECT id, status FROM images WHERE id = ?
		UNION ALL SELECT i.id, i.status FROM image_aliases a JOIN images i ON i.id = a.image_id WHERE a.name = ? LIMIT 1`, ref, ref).Scan(&imageID, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("%w: no image or image alias named %q", ErrNotFound, ref)
	}
	if err != nil {
		return "", err
	}
	if status != "ready" {
		return "", ErrImageNotReady
	}
	return imageID, nil
}

func resolveProfile(ctx context.Context, q queryer, ownerID, ref string, explicit bool) (any, error) {
	var revisionID, state string
	err := q.QueryRowContext(ctx, `SELECT r.id, r.state FROM profile_revisions r JOIN profiles p ON p.id = r.profile_id
		WHERE p.owner_id = ? AND (p.id = ? OR p.name = ?) ORDER BY r.state = 'ready' DESC, r.number DESC LIMIT 1`, ownerID, ref, ref).Scan(&revisionID, &state)
	if errors.Is(err, sql.ErrNoRows) {
		if !explicit {
			return nil, nil
		}
		return nil, fmt.Errorf("%w: no profile named %q", ErrNotFound, ref)
	}
	if err != nil {
		return nil, err
	}
	if state != "ready" {
		return nil, fmt.Errorf("%w: profile %q has no ready revision yet (latest is %s)", ErrProfileNotReady, ref, state)
	}
	return revisionID, nil
}

func authorizedKeys(ctx context.Context, q queryer, ownerID string) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT public_key FROM ssh_keys WHERE owner_id = ? ORDER BY created_at, rowid`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("%w: no SSH keys registered; run boxfacectl login to register this device's key", ErrInvalid)
	}
	return keys, nil
}

func defaults(ctx context.Context, q queryer, ownerID string) (Defaults, error) {
	var d Defaults
	err := q.QueryRowContext(ctx, `SELECT image, vcpus, memory_mib, disk_gib, profile FROM owner_defaults WHERE owner_id = ?`, ownerID).
		Scan(&d.Image, &d.VCPUs, &d.MemoryMiB, &d.DiskGiB, &d.Profile)
	if errors.Is(err, sql.ErrNoRows) {
		return builtinDefaults, nil
	}
	return d, err
}

func (s *Service) Defaults(ctx context.Context, ownerID string) (Defaults, error) {
	return defaults(ctx, s.db, ownerID)
}

func (s *Service) DefaultsSaved(ctx context.Context, ownerID string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM owner_defaults WHERE owner_id = ?`, ownerID).Scan(&n)
	return n > 0, err
}

func (s *Service) SetDefaults(ctx context.Context, ownerID string, d Defaults) error {
	if d.Image == "" || d.VCPUs < 1 || d.MemoryMiB < 128 || d.DiskGiB < 1 {
		return fmt.Errorf("%w: image is required; vcpus must be at least 1, memory_mib at least 128, disk_gib at least 1", ErrInvalid)
	}
	if d.Profile == "" {
		d.Profile = builtinDefaults.Profile
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO owner_defaults (owner_id, image, vcpus, memory_mib, disk_gib, profile, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (owner_id) DO UPDATE SET image = excluded.image, vcpus = excluded.vcpus, memory_mib = excluded.memory_mib,
		disk_gib = excluded.disk_gib, profile = excluded.profile, updated_at = excluded.updated_at`, ownerID, d.Image, d.VCPUs, d.MemoryMiB, d.DiskGiB, d.Profile, timestamp())
	return err
}

func (s *Service) Start(ctx context.Context, ownerID, idempotencyKey, environmentID string) (Accepted, error) {
	return s.submitForEnvironment(ctx, ownerID, idempotencyKey, TypeStart, targetParams{EnvironmentID: environmentID}, "running")
}

func (s *Service) Stop(ctx context.Context, ownerID, idempotencyKey, environmentID string) (Accepted, error) {
	return s.submitForEnvironment(ctx, ownerID, idempotencyKey, TypeStop, targetParams{EnvironmentID: environmentID}, "stopped")
}

func (s *Service) Delete(ctx context.Context, ownerID, idempotencyKey, environmentID string, purge bool) (Accepted, error) {
	return s.submitForEnvironment(ctx, ownerID, idempotencyKey, TypeDelete, targetParams{EnvironmentID: environmentID, Purge: purge}, "deleted")
}

func (s *Service) submitForEnvironment(ctx context.Context, ownerID, idempotencyKey, opType string, params targetParams, desired string) (Accepted, error) {
	return s.submit(ctx, ownerID, idempotencyKey, opType, params, func(tx *sql.Tx, opID string) (string, any, error) {
		var current, retention string
		err := tx.QueryRowContext(ctx, `SELECT e.desired_state, d.retention FROM environments e JOIN data_disks d ON d.environment_id = e.id WHERE e.id = ? AND e.owner_id = ?`,
			params.EnvironmentID, ownerID).Scan(&current, &retention)
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil, ErrNotFound
		}
		if err != nil {
			return "", nil, err
		}
		purgingRetainedDisk := opType == TypeDelete && params.Purge && retention == "retained"
		if current == "deleted" && !purgingRetainedDisk {
			return "", nil, ErrEnvironmentDeleted
		}
		if _, err := tx.ExecContext(ctx, `UPDATE environments SET desired_state = ?, updated_at = ? WHERE id = ?`, desired, timestamp(), params.EnvironmentID); err != nil {
			return "", nil, err
		}
		if opType == TypeDelete {
			return params.EnvironmentID, nil, audit(ctx, tx, ownerID, "environment.delete", params.EnvironmentID, opID)
		}
		if opType == TypeStart {
			keys, err := authorizedKeys(ctx, tx, ownerID)
			if err != nil {
				return "", nil, err
			}
			params.AuthorizedKeys = keys
			return params.EnvironmentID, params, nil
		}
		return params.EnvironmentID, nil, nil
	})
}

func (s *Service) submit(ctx context.Context, ownerID, idempotencyKey, opType string, params any, prepare func(tx *sql.Tx, opID string) (string, any, error)) (Accepted, error) {
	encoded, err := json.Marshal(params)
	if err != nil {
		return Accepted{}, err
	}
	sum := sha256.Sum256(append([]byte(opType+"\x00"), encoded...))
	hash := hex.EncodeToString(sum[:])

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Accepted{}, err
	}
	defer tx.Rollback()

	var key any
	if idempotencyKey != "" {
		key = idempotencyKey
		var existing Accepted
		var existingHash string
		err := tx.QueryRowContext(ctx, `SELECT id, target_id, request_hash FROM operations WHERE owner_id = ? AND type = ? AND idempotency_key = ?`,
			ownerID, opType, idempotencyKey).Scan(&existing.OperationID, &existing.EnvironmentID, &existingHash)
		if err == nil {
			if existingHash != hash {
				return Accepted{}, ErrIdempotencyConflict
			}
			return existing, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return Accepted{}, err
		}
	}

	opID := id.New()
	now := timestamp()
	if _, err := tx.ExecContext(ctx, `INSERT INTO operations (id, owner_id, type, target_id, idempotency_key, request_hash, params, state, created_at, updated_at)
		VALUES (?, ?, ?, '', ?, ?, ?, 'queued', ?, ?)`, opID, ownerID, opType, key, hash, string(encoded), now, now); err != nil {
		return Accepted{}, err
	}
	targetID, stored, err := prepare(tx, opID)
	if err != nil {
		return Accepted{}, err
	}
	if stored != nil {
		b, err := json.Marshal(stored)
		if err != nil {
			return Accepted{}, err
		}
		encoded = b
	}
	if _, err := tx.ExecContext(ctx, `UPDATE operations SET target_id = ?, params = ? WHERE id = ?`, targetID, string(encoded), opID); err != nil {
		return Accepted{}, err
	}
	if err := tx.Commit(); err != nil {
		return Accepted{}, err
	}
	s.runner.Kick()
	return Accepted{OperationID: opID, EnvironmentID: targetID}, nil
}

func (s *Service) Operation(ctx context.Context, ownerID, id string) (Operation, error) {
	op := Operation{ID: id, CompletedSteps: []string{}}
	err := s.db.QueryRowContext(ctx, `SELECT type, target_id, state, error_code, error_message FROM operations WHERE id = ? AND owner_id = ?`, id, ownerID).
		Scan(&op.Type, &op.TargetID, &op.State, &op.ErrorCode, &op.ErrorMessage)
	if errors.Is(err, sql.ErrNoRows) {
		return Operation{}, ErrNotFound
	}
	if err != nil {
		return Operation{}, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT step FROM operation_steps WHERE operation_id = ? ORDER BY completed_at, rowid`, id)
	if err != nil {
		return Operation{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var step string
		if err := rows.Scan(&step); err != nil {
			return Operation{}, err
		}
		op.CompletedSteps = append(op.CompletedSteps, step)
	}
	return op, rows.Err()
}

func (s *Service) Environment(ctx context.Context, ownerID, id string) (Environment, error) {
	env := Environment{ID: id}
	var profile sql.NullString
	var number, latest sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT e.name, e.image_id, e.vcpus, e.memory_mib, e.desired_state, e.observed_state, e.error, d.id, d.retention,
			p.name, r.number, (SELECT max(number) FROM profile_revisions WHERE profile_id = p.id AND state = 'ready')
		FROM environments e JOIN data_disks d ON d.environment_id = e.id
		LEFT JOIN profile_revisions r ON r.id = e.profile_revision_id LEFT JOIN profiles p ON p.id = r.profile_id
		WHERE e.id = ? AND e.owner_id = ?`, id, ownerID).
		Scan(&env.Name, &env.ImageID, &env.VCPUs, &env.MemoryMiB, &env.DesiredState, &env.ObservedState, &env.Error, &env.DiskID, &env.DiskRetention,
			&profile, &number, &latest)
	env.Profile, env.ProfileNumber, env.ProfileLatest = profile.String, int(number.Int64), int(latest.Int64)
	if errors.Is(err, sql.ErrNoRows) {
		return Environment{}, ErrNotFound
	}
	return env, err
}

func (s *Service) StartedKeys(ctx context.Context, ownerID, environmentID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT type, params FROM operations WHERE owner_id = ? AND target_id = ? AND type IN (?, ?) AND state = 'succeeded' ORDER BY rowid DESC`,
		ownerID, environmentID, TypeCreate, TypeStart)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var opType, params string
		if err := rows.Scan(&opType, &params); err != nil {
			return nil, err
		}
		var p struct {
			AuthorizedKeys []string `json:"authorized_keys"`
		}
		if err := json.Unmarshal([]byte(params), &p); err != nil {
			return nil, err
		}
		if len(p.AuthorizedKeys) > 0 {
			return p.AuthorizedKeys, nil
		}
	}
	return nil, rows.Err()
}

func (s *Service) checkCapacity(ctx context.Context, tx *sql.Tx, req CreateRequest) error {
	var envs, vcpus, memory, disk int
	if err := tx.QueryRowContext(ctx, `SELECT count(*), coalesce(sum(vcpus), 0), coalesce(sum(memory_mib), 0) FROM environments WHERE observed_state <> 'deleted'`).
		Scan(&envs, &vcpus, &memory); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, `SELECT coalesce(sum(size_gib), 0) FROM data_disks WHERE retention <> 'purged'`).Scan(&disk); err != nil {
		return err
	}
	for _, c := range []struct {
		name           string
		used, add, max int
	}{
		{"environments", envs, 1, s.capacity.Environments},
		{"vcpus", vcpus, req.VCPUs, s.capacity.VCPUs},
		{"memory_mib", memory, req.MemoryMiB, s.capacity.MemoryMiB},
		{"disk_gib", disk, req.DiskGiB, s.capacity.DiskGiB},
	} {
		if c.max > 0 && c.used+c.add > c.max {
			return fmt.Errorf("%w: %s would be %d, limit %d", ErrCapacity, c.name, c.used+c.add, c.max)
		}
	}
	return nil
}

func (s *Service) Environments(ctx context.Context, ownerID string) ([]Environment, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT e.id FROM environments e JOIN data_disks d ON d.environment_id = e.id
		WHERE e.owner_id = ? AND NOT (e.observed_state = 'deleted' AND d.retention = 'purged') ORDER BY e.created_at, e.rowid`, ownerID)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	envs := []Environment{}
	for _, id := range ids {
		env, err := s.Environment(ctx, ownerID, id)
		if err != nil {
			return nil, err
		}
		envs = append(envs, env)
	}
	return envs, nil
}

func audit(ctx context.Context, tx *sql.Tx, ownerID, action, targetID, opID string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO audit_events (owner_id, action, target_id, operation_id, created_at) VALUES (?, ?, ?, ?, ?)`,
		ownerID, action, targetID, opID, timestamp())
	return err
}

func timestamp() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}
