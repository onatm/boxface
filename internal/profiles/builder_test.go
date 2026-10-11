package profiles

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/onatm/boxface/internal/lifecycle"
)

type fakeWorker struct {
	started  []lifecycle.BuildSpec
	status   map[string]lifecycle.BuildStatus
	startErr error
}

func (f *fakeWorker) StartBuild(_ context.Context, spec lifecycle.BuildSpec) error {
	if f.startErr != nil {
		return f.startErr
	}
	f.started = append(f.started, spec)
	f.status[spec.BuildID] = lifecycle.BuildStatus{State: "running"}
	return nil
}

func (f *fakeWorker) BuildStatus(_ context.Context, id string) (lifecycle.BuildStatus, error) {
	if st, ok := f.status[id]; ok {
		return st, nil
	}
	return lifecycle.BuildStatus{State: "unknown"}, nil
}

func revision(t *testing.T, s *Store, ownerID, profileID string) Revision {
	t.Helper()
	p, err := s.Get(context.Background(), ownerID, profileID)
	if err != nil {
		t.Fatal(err)
	}
	return *p.Latest
}

func TestBuilder(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	w := &fakeWorker{status: map[string]lifecycle.BuildStatus{}}
	b := NewBuilder(s.db, w, Limits{VCPUs: 2, MemoryMiB: 2048, TimeoutSec: 3600})

	p, _ := s.Create(ctx, "owner-1", "main", Spec{Packages: []string{"ripgrep"}, Dotfiles: &Dotfiles{Repo: "https://example.com/d.git", Ref: "main"}})
	other, _ := s.Create(ctx, "owner-1", "other", Spec{Packages: []string{"jq"}})
	if err := b.Step(ctx); err != nil {
		t.Fatal(err)
	}
	if len(w.started) != 1 {
		t.Fatalf("started %d builds", len(w.started))
	}
	got := w.started[0]
	if got.BuildID != p.Latest.BuildKey || got.OwnerID != "owner-1" || got.MemoryMiB != 2048 || got.DotfilesRepo != "https://example.com/d.git" || got.DotfilesRef != "main" ||
		!strings.Contains(got.Files["flake.nix"], "ripgrep") || got.Files["flake.lock"] == "" {
		t.Fatalf("build spec = %+v", got)
	}
	if r := revision(t, s, "owner-1", p.ID); r.State != "building" {
		t.Fatalf("state = %s", r.State)
	}
	if err := b.Step(ctx); err != nil || len(w.started) != 1 {
		t.Fatalf("a second build started while one runs: %d, %v", len(w.started), err)
	}

	w.status[got.BuildID] = lifecycle.BuildStatus{State: "ready", DotfilesCommit: "c0ffee", LogTail: "built"}
	if err := b.Step(ctx); err != nil {
		t.Fatal(err)
	}
	if r := revision(t, s, "owner-1", p.ID); r.State != "ready" || r.DotfilesCommit != "c0ffee" {
		t.Fatalf("after ready = %+v", r)
	}
	if len(w.started) != 2 || w.started[1].BuildID != other.Latest.BuildKey {
		t.Fatalf("the next pending revision did not start: %+v", w.started)
	}

	same, _ := s.AddRevision(ctx, "owner-1", p.ID, Spec{Packages: []string{"ripgrep", "tmux"}, Dotfiles: &Dotfiles{Repo: "https://example.com/d.git", Ref: "main"}})
	w.status[other.Latest.BuildKey] = lifecycle.BuildStatus{State: "failed", Error: "nix build failed", LogTail: "error: attribute 'jqq' missing"}
	b.Step(ctx)
	b.Step(ctx)
	if r := revision(t, s, "owner-1", other.ID); r.State != "failed" || r.Error != "nix build failed" || !strings.Contains(r.LogTail, "jqq") {
		t.Fatalf("failed build = %+v", r)
	}
	if r := revision(t, s, "owner-1", p.ID); r.Number != same.Number || r.State != "ready" || r.DotfilesCommit != "c0ffee" || len(w.started) != 2 {
		t.Fatalf("identical spec was not reused: %+v, %d builds", r, len(w.started))
	}

	w.startErr = errors.New("another build (x) is running")
	third, _ := s.Create(ctx, "owner-1", "third", Spec{Packages: []string{"fd"}})
	b.Step(ctx)
	if r := revision(t, s, "owner-1", third.ID); r.State != "pending" {
		t.Fatalf("busy worker: state = %s", r.State)
	}
	w.startErr = errors.New("builder image: no such file")
	b.Step(ctx)
	if r := revision(t, s, "owner-1", third.ID); r.State != "failed" || !strings.Contains(r.Error, "builder image") {
		t.Fatalf("start failure = %+v", r)
	}

	w.startErr = nil
	lost, _ := s.Create(ctx, "owner-2", "lost", Spec{Packages: []string{"htop"}})
	b.Step(ctx)
	delete(w.status, lost.Latest.BuildKey)
	b.Step(ctx)
	if r := revision(t, s, "owner-2", lost.ID); r.State != "building" || len(w.started) != 4 {
		t.Fatalf("lost build was not restarted: %+v, %d builds", r, len(w.started))
	}
}
