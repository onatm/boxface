package secrets

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/onatm/boxface/internal/lifecycle"
)

type fakeSetter struct {
	mu   sync.Mutex
	sets map[string]lifecycle.Credentials
	fail error
}

func (f *fakeSetter) SetCredentials(_ context.Context, id string, creds lifecycle.Credentials) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return f.fail
	}
	f.sets[id] = creds
	return nil
}

type fakeRecorder struct{ events []string }

func (f *fakeRecorder) Record(_ context.Context, ownerID, action, tokenID, targetID, remote string) error {
	f.events = append(f.events, strings.Join([]string{ownerID, action, tokenID, targetID, remote}, "|"))
	return nil
}

func TestPusherDeliversToRunningEnvironments(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	for _, q := range []string{
		`INSERT INTO images (id, digest, status, created_at) VALUES ('image-1', 'sha256:x', 'ready', 'x')`,
		`INSERT INTO environments (id, owner_id, name, image_id, vcpus, memory_mib, desired_state, observed_state, created_at, updated_at) VALUES
			('env-run', 'owner-1', 'a', 'image-1', 1, 512, 'running', 'running', 'x', 'x'),
			('env-stop', 'owner-1', 'b', 'image-1', 1, 512, 'stopped', 'stopped', 'x', 'x'),
			('env-other', 'owner-2', 'c', 'image-1', 1, 512, 'running', 'running', 'x', 'x')`,
	} {
		if _, err := s.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Create(ctx, "owner-1", "claude", claudeToken, claudeBinding); err != nil {
		t.Fatal(err)
	}
	w := &fakeSetter{sets: map[string]lifecycle.Credentials{}}
	rec := &fakeRecorder{}
	p := NewPusher(s.db, s, w, rec)

	p.PushOwner(ctx, "owner-1")
	if len(w.sets) != 1 || w.sets["env-run"]["claude-code"]["CLAUDE_CODE_OAUTH_TOKEN"] != claudeToken {
		t.Fatalf("pushed = %v", w.sets)
	}
	if err := p.Push(ctx, "env-other"); err != nil {
		t.Fatal(err)
	}
	if len(w.sets["env-other"]) != 0 {
		t.Fatalf("another owner's environment got %v", w.sets["env-other"])
	}
	if err := p.Push(ctx, "missing"); !errors.Is(err, lifecycle.ErrNotFound) {
		t.Fatalf("missing environment: err = %v", err)
	}
	p.Reconcile(ctx, []string{"missing", "env-stop"})
	if _, ok := w.sets["env-stop"]; !ok {
		t.Fatal("reconcile did not push to a VM the worker reported without credentials")
	}
	if len(rec.events) == 0 || rec.events[0] != "owner-1|secret.deliver||secret:claude@env-run|" {
		t.Fatalf("audit = %v", rec.events)
	}
	for _, e := range rec.events {
		if strings.Contains(e, claudeToken) {
			t.Fatal("the value appears in an audit event")
		}
	}

	w.fail = errors.New("worker unavailable")
	p.PushOwner(ctx, "owner-1")
}
