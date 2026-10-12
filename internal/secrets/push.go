package secrets

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"

	"github.com/onatm/boxface/internal/lifecycle"
)

type Setter interface {
	SetCredentials(ctx context.Context, environmentID string, creds lifecycle.Credentials) error
}

type Recorder interface {
	Record(ctx context.Context, ownerID, action, tokenID, targetID, remote string) error
}

type Pusher struct {
	db     *sql.DB
	store  *Store
	worker Setter
	audit  Recorder
}

func NewPusher(db *sql.DB, store *Store, worker Setter, audit Recorder) *Pusher {
	return &Pusher{db: db, store: store, worker: worker, audit: audit}
}

func (p *Pusher) Push(ctx context.Context, environmentID string) error {
	var ownerID string
	err := p.db.QueryRowContext(ctx, `SELECT owner_id FROM environments WHERE id = ?`, environmentID).Scan(&ownerID)
	if errors.Is(err, sql.ErrNoRows) {
		return lifecycle.ErrNotFound
	}
	if err != nil {
		return err
	}
	return p.push(ctx, ownerID, environmentID)
}

func (p *Pusher) push(ctx context.Context, ownerID, environmentID string) error {
	r, err := p.store.Resolve(ctx, ownerID)
	if err != nil {
		return err
	}
	if err := p.worker.SetCredentials(ctx, environmentID, r.Env); err != nil {
		return err
	}
	for _, name := range r.Secrets {
		if err := p.audit.Record(ctx, ownerID, "secret.deliver", "", "secret:"+name+"@"+environmentID, ""); err != nil {
			return err
		}
	}
	return nil
}

func (p *Pusher) PushOwner(ctx context.Context, ownerID string) {
	rows, err := p.db.QueryContext(ctx, `SELECT id FROM environments WHERE owner_id = ? AND observed_state IN ('starting', 'running')`, ownerID)
	if err != nil {
		slog.Error("list environments for credential push", "error", err)
		return
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, id := range ids {
		if err := p.push(ctx, ownerID, id); err != nil {
			slog.Warn("credential push failed; reconcile will retry", "environment", id, "error", err)
		}
	}
}

func (p *Pusher) Reconcile(ctx context.Context, missing []string) {
	for _, id := range missing {
		if err := p.Push(ctx, id); err != nil && !errors.Is(err, lifecycle.ErrNotFound) {
			slog.Warn("credential push failed; will retry", "environment", id, "error", err)
		}
	}
}
