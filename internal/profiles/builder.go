package profiles

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/onatm/boxface/internal/lifecycle"
)

type Worker interface {
	StartBuild(ctx context.Context, spec lifecycle.BuildSpec) error
	BuildStatus(ctx context.Context, buildID string) (lifecycle.BuildStatus, error)
	ProfileDrives(ctx context.Context) ([]string, error)
	RemoveProfileDrive(ctx context.Context, key string) error
}

const pruneGrace = 15 * time.Minute

type Limits struct {
	VCPUs      int
	MemoryMiB  int
	TimeoutSec int
}

type Builder struct {
	db     *sql.DB
	worker Worker
	limits Limits
	Poll   time.Duration
	kick   chan struct{}
	dirty  bool
}

func NewBuilder(db *sql.DB, worker Worker, limits Limits) *Builder {
	return &Builder{db: db, worker: worker, limits: limits, Poll: 5 * time.Second, kick: make(chan struct{}, 1), dirty: true}
}

func (b *Builder) Kick() {
	select {
	case b.kick <- struct{}{}:
	default:
	}
}

func (b *Builder) Run(ctx context.Context) {
	for {
		if err := b.Step(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("profile builder step failed; will retry", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-b.kick:
			b.dirty = true
		case <-time.After(b.Poll):
		}
	}
}

func (b *Builder) Step(ctx context.Context) error {
	if b.dirty {
		if err := b.Prune(ctx); err != nil {
			return err
		}
		b.dirty = false
	}
	busy, err := b.followBuilding(ctx)
	if err != nil || busy {
		return err
	}
	return b.startPending(ctx)
}

func (b *Builder) Prune(ctx context.Context) error {
	keep, err := b.keys(ctx, `SELECT r.build_key FROM profile_revisions r JOIN profiles p ON p.id = r.profile_id
			WHERE r.state = 'ready' AND p.deleted_at IS NULL
			AND r.number = (SELECT max(number) FROM profile_revisions WHERE profile_id = p.id AND state = 'ready')
		UNION SELECT r.build_key FROM profile_revisions r JOIN environments e ON e.profile_revision_id = r.id WHERE e.observed_state <> 'deleted'
		UNION SELECT build_key FROM profile_revisions WHERE state IN ('pending', 'building') OR updated_at > ?`,
		time.Now().Add(-pruneGrace).UTC().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	drives, err := b.worker.ProfileDrives(ctx)
	if err != nil {
		return err
	}
	for _, key := range drives {
		if slices.Contains(keep, key) {
			continue
		}
		if err := b.worker.RemoveProfileDrive(ctx, key); err != nil {
			slog.Warn("could not remove an unused profile drive", "build_key", key, "error", err)
			continue
		}
		if _, err := b.db.ExecContext(ctx, `UPDATE profile_revisions SET pruned_at = ? WHERE build_key = ? AND state = 'ready'`, timestamp(), key); err != nil {
			return err
		}
		slog.Info("removed an unused profile drive", "build_key", key)
	}
	return nil
}

func (b *Builder) followBuilding(ctx context.Context) (bool, error) {
	keys, err := b.keys(ctx, `SELECT DISTINCT build_key FROM profile_revisions WHERE state = 'building'`)
	if err != nil {
		return false, err
	}
	busy := false
	for _, key := range keys {
		st, err := b.worker.BuildStatus(ctx, key)
		if err != nil {
			return true, err
		}
		switch st.State {
		case "running":
			busy = true
		case "ready", "failed":
			if err := b.finish(ctx, key, "building", st); err != nil {
				return true, err
			}
		default:
			if _, err := b.db.ExecContext(ctx, `UPDATE profile_revisions SET state = 'pending', updated_at = ? WHERE build_key = ? AND state = 'building'`, timestamp(), key); err != nil {
				return true, err
			}
		}
	}
	return busy, nil
}

func (b *Builder) finish(ctx context.Context, key, from string, st lifecycle.BuildStatus) error {
	_, err := b.db.ExecContext(ctx, `UPDATE profile_revisions SET state = ?, error = ?, log_tail = ?, dotfiles_commit = ?, pruned_at = NULL, updated_at = ? WHERE build_key = ? AND state = ?`,
		st.State, st.Error, st.LogTail, st.DotfilesCommit, timestamp(), key, from)
	b.dirty = true
	return err
}

func (b *Builder) keys(ctx context.Context, query string, args ...any) ([]string, error) {
	rows, err := b.db.QueryContext(ctx, query, args...)
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
	return keys, rows.Err()
}

func (b *Builder) startPending(ctx context.Context) error {
	var key, specJSON, ownerID string
	err := b.db.QueryRowContext(ctx, `SELECT r.build_key, r.spec, p.owner_id FROM profile_revisions r JOIN profiles p ON p.id = r.profile_id
		WHERE r.state = 'pending' ORDER BY r.created_at, r.rowid LIMIT 1`).Scan(&key, &specJSON, &ownerID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var ready lifecycle.BuildStatus
	err = b.db.QueryRowContext(ctx, `SELECT dotfiles_commit, log_tail FROM profile_revisions WHERE build_key = ? AND state = 'ready' AND pruned_at IS NULL LIMIT 1`, key).
		Scan(&ready.DotfilesCommit, &ready.LogTail)
	if err == nil {
		ready.State = "ready"
		return b.finish(ctx, key, "pending", ready)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var spec Spec
	if err := json.Unmarshal([]byte(specJSON), &spec); err != nil {
		return err
	}
	build := lifecycle.BuildSpec{BuildID: key, OwnerID: ownerID, Files: Flake(spec), VCPUs: b.limits.VCPUs, MemoryMiB: b.limits.MemoryMiB, TimeoutSec: b.limits.TimeoutSec}
	if spec.Dotfiles != nil {
		build.DotfilesRepo, build.DotfilesRef = spec.Dotfiles.Repo, spec.Dotfiles.Ref
	}
	if err := b.worker.StartBuild(ctx, build); err != nil {
		if strings.Contains(err.Error(), "another build") || errors.Is(err, lifecycle.ErrWorkerUnavailable) {
			return err
		}
		return b.finish(ctx, key, "pending", lifecycle.BuildStatus{State: "failed", Error: "could not start the build: " + err.Error()})
	}
	_, err = b.db.ExecContext(ctx, `UPDATE profile_revisions SET state = 'building', error = '', log_tail = '', updated_at = ? WHERE build_key = ? AND state = 'pending'`, timestamp(), key)
	return err
}
