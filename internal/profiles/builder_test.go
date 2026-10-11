package profiles

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/onatm/boxface/internal/lifecycle"
)

type fakeWorker struct {
	started  []lifecycle.BuildSpec
	status   map[string]lifecycle.BuildStatus
	startErr error
	drives   []string
	removed  []string
}

func (f *fakeWorker) ProfileDrives(context.Context) ([]string, error) {
	return slices.Clone(f.drives), nil
}

func (f *fakeWorker) RemoveProfileDrive(_ context.Context, key string) error {
	f.removed = append(f.removed, key)
	f.drives = slices.DeleteFunc(f.drives, func(k string) bool { return k == key })
	return nil
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

func TestPruneAndDelete(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	w := &fakeWorker{status: map[string]lifecycle.BuildStatus{}}
	b := NewBuilder(s.db, w, Limits{VCPUs: 2, MemoryMiB: 2048, TimeoutSec: 3600})
	s.db.Exec(`INSERT INTO images (id, digest, status, created_at) VALUES ('img', 'sha256:x', 'ready', 'x')`)

	main, _ := s.Create(ctx, "owner-1", "main", Spec{Packages: []string{"jq"}})
	r2, _ := s.AddRevision(ctx, "owner-1", main.ID, Spec{Packages: []string{"jq", "fd"}})
	r3, _ := s.AddRevision(ctx, "owner-1", main.ID, Spec{Packages: []string{"jq", "fd", "htop"}})
	old := "2026-01-01T00:00:00Z"
	s.db.Exec(`UPDATE profile_revisions SET state = 'ready', updated_at = ?`, old)
	s.db.Exec(`INSERT INTO environments (id, owner_id, name, image_id, vcpus, memory_mib, desired_state, observed_state, created_at, updated_at, profile_revision_id)
		VALUES ('env-old', 'owner-1', 'old', 'img', 1, 512, 'running', 'running', 'x', 'x', ?)`, r2.ID)
	w.drives = []string{main.Latest.BuildKey, r2.BuildKey, r3.BuildKey, "orphan-key"}

	if err := b.Prune(ctx); err != nil {
		t.Fatal(err)
	}
	if strings.Join(w.removed, ",") != main.Latest.BuildKey+",orphan-key" {
		t.Fatalf("removed = %v; want revision 1 and the orphan, keeping 2 (in use) and 3 (latest)", w.removed)
	}
	got, _ := s.Get(ctx, "owner-1", main.ID)
	if !got.Revisions[2].Pruned || got.Revisions[1].Pruned || got.Revisions[0].Pruned {
		t.Fatalf("pruned flags = %v %v %v", got.Revisions[0].Pruned, got.Revisions[1].Pruned, got.Revisions[2].Pruned)
	}

	again, _ := s.AddRevision(ctx, "owner-1", main.ID, Spec{Packages: []string{"jq"}})
	b.dirty = false
	b.Step(ctx)
	if r := revision(t, s, "owner-1", main.ID); r.Number != again.Number || r.State != "building" {
		t.Fatalf("a spec whose drive was pruned must rebuild: %+v", r)
	}

	if _, err := s.Delete(ctx, "owner-1", main.ID, "main"); !errors.Is(err, ErrInUse) {
		t.Fatalf("delete the default: err = %v", err)
	}
	if _, err := s.Delete(ctx, "owner-1", main.ID, "other"); !errors.Is(err, ErrInUse) || !strings.Contains(err.Error(), "old") {
		t.Fatalf("delete while in use: err = %v", err)
	}
	s.db.Exec(`UPDATE environments SET observed_state = 'deleted', desired_state = 'deleted'`)
	if _, err := s.Delete(ctx, "owner-2", main.ID, "other"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete by another owner: err = %v", err)
	}
	if name, err := s.Delete(ctx, "owner-1", main.ID, "other"); err != nil || name != "main" {
		t.Fatalf("delete = %q, %v", name, err)
	}
	if list, _ := s.List(ctx, "owner-1"); len(list) != 0 {
		t.Fatalf("deleted profile listed: %+v", list)
	}
	if _, err := s.Create(ctx, "owner-1", "main", Spec{}); err != nil {
		t.Fatalf("the name was not freed: %v", err)
	}
}
