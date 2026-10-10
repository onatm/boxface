package lifecycle

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

type job struct {
	id     string
	opType string
	params targetParams
	env    VMSpec
	disk   DiskSpec
}

type step struct {
	name string
	run  func(ctx context.Context, w Worker, j job) error
}

type kind struct {
	during, after string
	steps         []step
}

var (
	createDisk    = step{"create_disk", func(ctx context.Context, w Worker, j job) error { return w.CreateDisk(ctx, j.disk) }}
	createNetwork = step{"create_network", func(ctx context.Context, w Worker, j job) error { return w.CreateNetwork(ctx, j.env.EnvironmentID) }}
	startVM       = step{"start_vm", func(ctx context.Context, w Worker, j job) error { return w.StartVM(ctx, j.env) }}
	waitReady     = step{"wait_ready", func(ctx context.Context, w Worker, j job) error { return w.WaitReady(ctx, j.env.EnvironmentID) }}
	stopVM        = step{"stop_vm", func(ctx context.Context, w Worker, j job) error { return w.StopVM(ctx, j.env.EnvironmentID) }}
	removeNetwork = step{"remove_network", func(ctx context.Context, w Worker, j job) error { return w.RemoveNetwork(ctx, j.env.EnvironmentID) }}
	purgeDisk     = step{"purge_disk", func(ctx context.Context, w Worker, j job) error {
		if !j.params.Purge {
			return nil
		}
		return w.RemoveDisk(ctx, j.disk.DiskID)
	}}
)

var kinds = map[string]kind{
	TypeCreate: {"starting", "running", []step{createDisk, createNetwork, startVM, waitReady}},
	TypeStart:  {"starting", "running", []step{createNetwork, startVM, waitReady}},
	TypeStop:   {"stopping", "stopped", []step{stopVM, removeNetwork}},
	TypeDelete: {"deleting", "deleted", []step{stopVM, removeNetwork, purgeDisk}},
}

type Inventory interface {
	ActiveVMs(ctx context.Context) (map[string]bool, error)
}

type Runner struct {
	ctx        context.Context
	db         *sql.DB
	worker     Worker
	RetryDelay time.Duration

	mu     sync.Mutex
	active map[string]bool
	wg     sync.WaitGroup
}

func NewRunner(ctx context.Context, db *sql.DB, worker Worker) *Runner {
	return &Runner{ctx: ctx, db: db, worker: worker, RetryDelay: 2 * time.Second, active: map[string]bool{}}
}

func (r *Runner) Kick() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ctx.Err() != nil {
		return
	}

	type pending struct{ id, target string }
	var next []pending
	rows, err := r.db.QueryContext(r.ctx, `SELECT id, target_id FROM operations WHERE state IN ('queued', 'running') ORDER BY rowid`)
	if err != nil {
		slog.Error("list pending operations", "error", err)
		return
	}
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.id, &p.target); err != nil {
			slog.Error("scan pending operation", "error", err)
			rows.Close()
			return
		}
		next = append(next, p)
	}
	rows.Close()

	for _, p := range next {
		if r.active[p.target] {
			continue
		}
		r.active[p.target] = true
		r.wg.Add(1)
		go r.run(p.id, p.target)
	}
}

func (r *Runner) Wait() {
	r.wg.Wait()
}

func (r *Runner) run(id, target string) {
	defer r.wg.Done()
	if err := r.execute(id); err != nil {
		if r.ctx.Err() != nil {
			slog.Info("operation interrupted by shutdown", "operation", id, "error", err)
			return
		}
		slog.Error("operation failed internally", "operation", id, "error", err)
		if err := r.fail(id, target, "internal_error", err.Error(), "error"); err != nil {
			slog.Error("could not record operation failure; holding environment until restart", "operation", id, "environment", target, "error", err)
			return
		}
	}
	r.mu.Lock()
	delete(r.active, target)
	r.mu.Unlock()
	r.Kick()
}

func (r *Runner) execute(id string) error {
	ctx := r.ctx
	j, err := r.load(ctx, id)
	if err != nil {
		return err
	}
	k := kinds[j.opType]

	if _, err := r.db.ExecContext(ctx, `UPDATE operations SET state = 'running', updated_at = ? WHERE id = ?`, timestamp(), id); err != nil {
		return err
	}
	if err := r.setObserved(ctx, j.env.EnvironmentID, k.during, ""); err != nil {
		return err
	}

	done := map[string]bool{}
	rows, err := r.db.QueryContext(ctx, `SELECT step FROM operation_steps WHERE operation_id = ?`, id)
	if err != nil {
		return err
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		done[name] = true
	}
	rows.Close()

	for _, s := range k.steps {
		if done[s.name] {
			continue
		}
		if err := r.runStep(ctx, s, j); err != nil {
			if ctx.Err() != nil {
				return fmt.Errorf("step %s: %w", s.name, ctx.Err())
			}
			message := fmt.Sprintf("%s: %v", s.name, err)
			if j.opType != TypeCreate && j.opType != TypeStart {
				return r.fail(id, j.env.EnvironmentID, "step_failed", message, "error")
			}
			for _, cleanup := range []step{stopVM, removeNetwork} {
				if err := r.runStep(ctx, cleanup, j); err != nil {
					if ctx.Err() != nil {
						return fmt.Errorf("cleanup after %s: %w", s.name, ctx.Err())
					}
					return r.fail(id, j.env.EnvironmentID, "step_failed", fmt.Sprintf("%s; cleanup %s failed: %v", message, cleanup.name, err), "error")
				}
			}
			return r.fail(id, j.env.EnvironmentID, "step_failed", message, "stopped")
		}
		if _, err := r.db.ExecContext(ctx, `INSERT INTO operation_steps (operation_id, step, completed_at) VALUES (?, ?, ?)`, id, s.name, timestamp()); err != nil {
			return err
		}
	}
	return r.succeed(ctx, j, k)
}

func (r *Runner) runStep(ctx context.Context, s step, j job) error {
	for {
		err := s.run(ctx, r.worker, j)
		if !errors.Is(err, ErrWorkerUnavailable) {
			return err
		}
		slog.Warn("worker unavailable; retrying step", "operation", j.id, "step", s.name, "error", err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(r.RetryDelay):
		}
	}
}

func (r *Runner) load(ctx context.Context, id string) (job, error) {
	j := job{id: id}
	var params string
	err := r.db.QueryRowContext(ctx, `SELECT o.type, o.params, e.id, e.image_id, e.vcpus, e.memory_mib, d.id, d.size_gib
		FROM operations o JOIN environments e ON e.id = o.target_id JOIN data_disks d ON d.environment_id = e.id
		WHERE o.id = ?`, id).
		Scan(&j.opType, &params, &j.env.EnvironmentID, &j.env.ImageID, &j.env.VCPUs, &j.env.MemoryMiB, &j.disk.DiskID, &j.disk.SizeGiB)
	if err != nil {
		return job{}, fmt.Errorf("load operation %s: %w", id, err)
	}
	if _, ok := kinds[j.opType]; !ok {
		return job{}, fmt.Errorf("operation %s has unknown type %q", id, j.opType)
	}
	if j.opType == TypeCreate {
		var p createParams
		if err := json.Unmarshal([]byte(params), &p); err != nil {
			return job{}, fmt.Errorf("decode operation %s params: %w", id, err)
		}
		j.disk.AuthorizedKeys = p.AuthorizedKeys
	} else if err := json.Unmarshal([]byte(params), &j.params); err != nil {
		return job{}, fmt.Errorf("decode operation %s params: %w", id, err)
	}
	j.env.DiskID = j.disk.DiskID
	j.disk.EnvironmentID = j.env.EnvironmentID
	return j, nil
}

func (r *Runner) setObserved(ctx context.Context, environmentID, state, message string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE environments SET observed_state = ?, error = ?, updated_at = ? WHERE id = ?`, state, message, timestamp(), environmentID)
	return err
}

func (r *Runner) succeed(ctx context.Context, j job, k kind) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := timestamp()
	if _, err := tx.ExecContext(ctx, `UPDATE environments SET observed_state = ?, error = '', updated_at = ? WHERE id = ?`, k.after, now, j.env.EnvironmentID); err != nil {
		return err
	}
	if j.opType == TypeDelete {
		retention := "retained"
		if j.params.Purge {
			retention = "purged"
		}
		if _, err := tx.ExecContext(ctx, `UPDATE data_disks SET retention = ?, updated_at = ? WHERE id = ?`, retention, now, j.disk.DiskID); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE operations SET state = 'succeeded', updated_at = ? WHERE id = ?`, now, j.id); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Runner) fail(id, environmentID, code, message, observed string) error {
	tx, err := r.db.BeginTx(r.ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := timestamp()
	if _, err := tx.ExecContext(r.ctx, `UPDATE environments SET observed_state = ?, error = ?, updated_at = ? WHERE id = ?`, observed, message, now, environmentID); err != nil {
		return err
	}
	if observed == "stopped" {
		if _, err := tx.ExecContext(r.ctx, `UPDATE environments SET desired_state = 'stopped' WHERE id = ? AND desired_state = 'running'`, environmentID); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(r.ctx, `UPDATE operations SET state = 'failed', error_code = ?, error_message = ?, updated_at = ? WHERE id = ?`, code, message, now, id); err != nil {
		return err
	}
	return tx.Commit()
}

const reconciledMessage = "VM was not running when reconciled; the host may have restarted or the VM exited. Start the environment to run it again."

func (r *Runner) Reconcile(ctx context.Context, inv Inventory) ([]string, error) {
	active, err := inv.ActiveVMs(ctx)
	if err != nil {
		return nil, err
	}
	const idle = `observed_state = 'running' AND NOT EXISTS (SELECT 1 FROM operations o WHERE o.target_id = environments.id AND o.state IN ('queued', 'running'))`
	rows, err := r.db.QueryContext(ctx, `SELECT id FROM environments WHERE `+idle)
	if err != nil {
		return nil, err
	}
	var candidates []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		if !active[id] {
			candidates = append(candidates, id)
		}
	}
	rows.Close()

	var stopped []string
	for _, id := range candidates {
		r.mu.Lock()
		busy := r.active[id]
		r.active[id] = true
		r.mu.Unlock()
		if busy {
			continue
		}
		ok, err := r.reconcileOne(ctx, id, idle)
		r.mu.Lock()
		delete(r.active, id)
		r.mu.Unlock()
		if err != nil {
			return stopped, err
		}
		if ok {
			stopped = append(stopped, id)
		}
	}
	r.Kick()
	return stopped, nil
}

func (r *Runner) reconcileOne(ctx context.Context, id, idle string) (bool, error) {
	var still int
	if err := r.db.QueryRowContext(ctx, `SELECT count(*) FROM environments WHERE id = ? AND `+idle, id).Scan(&still); err != nil || still == 0 {
		return false, err
	}
	if err := r.worker.RemoveNetwork(ctx, id); err != nil {
		return false, fmt.Errorf("reconcile %s: %w", id, err)
	}
	res, err := r.db.ExecContext(ctx, `UPDATE environments SET observed_state = 'stopped', desired_state = 'stopped', error = ?, updated_at = ? WHERE id = ? AND `+idle,
		reconciledMessage, timestamp(), id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}
