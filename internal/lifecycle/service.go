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
}

type targetParams struct {
	EnvironmentID string `json:"environment_id"`
	Purge         bool   `json:"purge,omitempty"`
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
	if req.VCPUs < 1 || req.MemoryMiB < 128 || req.DiskGiB < 1 {
		return Accepted{}, fmt.Errorf("%w: vcpus must be at least 1, memory_mib at least 128, disk_gib at least 1", ErrInvalid)
	}
	return s.submit(ctx, ownerID, idempotencyKey, TypeCreate, req, func(tx *sql.Tx, opID string) (string, error) {
		var status string
		err := tx.QueryRowContext(ctx, `SELECT status FROM images WHERE id = ?`, req.ImageID).Scan(&status)
		if errors.Is(err, sql.ErrNoRows) {
			return "", fmt.Errorf("%w: image %s", ErrNotFound, req.ImageID)
		}
		if err != nil {
			return "", err
		}
		if status != "ready" {
			return "", ErrImageNotReady
		}
		var taken int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM environments WHERE owner_id = ? AND name = ? AND observed_state <> 'deleted'`, ownerID, req.Name).Scan(&taken); err != nil {
			return "", err
		}
		if taken > 0 {
			return "", ErrNameTaken
		}
		if err := s.checkCapacity(ctx, tx, req); err != nil {
			return "", err
		}
		envID, diskID, now := id.New(), id.New(), timestamp()
		if _, err := tx.ExecContext(ctx, `INSERT INTO environments (id, owner_id, name, image_id, vcpus, memory_mib, desired_state, observed_state, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, 'running', 'pending', ?, ?)`, envID, ownerID, req.Name, req.ImageID, req.VCPUs, req.MemoryMiB, now, now); err != nil {
			return "", err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO data_disks (id, owner_id, environment_id, size_gib, retention, created_at, updated_at)
			VALUES (?, ?, ?, ?, 'attached', ?, ?)`, diskID, ownerID, envID, req.DiskGiB, now, now); err != nil {
			return "", err
		}
		return envID, audit(ctx, tx, ownerID, "environment.create", envID, opID)
	})
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
	return s.submit(ctx, ownerID, idempotencyKey, opType, params, func(tx *sql.Tx, opID string) (string, error) {
		var current string
		err := tx.QueryRowContext(ctx, `SELECT desired_state FROM environments WHERE id = ? AND owner_id = ?`, params.EnvironmentID, ownerID).Scan(&current)
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrNotFound
		}
		if err != nil {
			return "", err
		}
		if current == "deleted" {
			return "", ErrEnvironmentDeleted
		}
		if _, err := tx.ExecContext(ctx, `UPDATE environments SET desired_state = ?, updated_at = ? WHERE id = ?`, desired, timestamp(), params.EnvironmentID); err != nil {
			return "", err
		}
		if opType == TypeDelete {
			return params.EnvironmentID, audit(ctx, tx, ownerID, "environment.delete", params.EnvironmentID, opID)
		}
		return params.EnvironmentID, nil
	})
}

func (s *Service) submit(ctx context.Context, ownerID, idempotencyKey, opType string, params any, prepare func(tx *sql.Tx, opID string) (string, error)) (Accepted, error) {
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
	targetID, err := prepare(tx, opID)
	if err != nil {
		return Accepted{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE operations SET target_id = ? WHERE id = ?`, targetID, opID); err != nil {
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
	err := s.db.QueryRowContext(ctx, `SELECT e.name, e.image_id, e.vcpus, e.memory_mib, e.desired_state, e.observed_state, e.error, d.id, d.retention
		FROM environments e JOIN data_disks d ON d.environment_id = e.id WHERE e.id = ? AND e.owner_id = ?`, id, ownerID).
		Scan(&env.Name, &env.ImageID, &env.VCPUs, &env.MemoryMiB, &env.DesiredState, &env.ObservedState, &env.Error, &env.DiskID, &env.DiskRetention)
	if errors.Is(err, sql.ErrNoRows) {
		return Environment{}, ErrNotFound
	}
	return env, err
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
