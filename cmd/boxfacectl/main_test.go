package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/onatm/boxface/internal/api"
	"github.com/onatm/boxface/internal/auth"
	"github.com/onatm/boxface/internal/images"
	"github.com/onatm/boxface/internal/lifecycle"
	"github.com/onatm/boxface/internal/store"
)

type gatedWorker struct {
	gate    chan struct{}
	entered chan struct{}
}

func (w *gatedWorker) CreateDisk(context.Context, lifecycle.DiskSpec) error { return nil }
func (w *gatedWorker) RemoveDisk(context.Context, string) error             { return nil }
func (w *gatedWorker) CreateNetwork(context.Context, string) error          { return nil }
func (w *gatedWorker) RemoveNetwork(context.Context, string) error          { return nil }
func (w *gatedWorker) StopVM(context.Context, string) error                 { return nil }
func (w *gatedWorker) WaitReady(context.Context, string) error              { return nil }
func (w *gatedWorker) StartVM(ctx context.Context, _ lifecycle.VMSpec) error {
	if w.gate == nil {
		return nil
	}
	w.entered <- struct{}{}
	select {
	case <-w.gate:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type harness struct {
	t         *testing.T
	server    string
	code      string
	importDir string
	config    string
	worker    *gatedWorker
	runner    *lifecycle.Runner
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "boxface.db"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	w := &gatedWorker{}
	runner := lifecycle.NewRunner(ctx, db, w)
	importDir := filepath.Join(dir, "import")
	os.MkdirAll(importDir, 0o755)
	os.MkdirAll(filepath.Join(dir, "images"), 0o755)
	authStore := auth.New(db)
	s := &api.Server{
		Auth:         authStore,
		Lifecycle:    lifecycle.NewService(db, runner, lifecycle.Capacity{}),
		Images:       images.NewRegistry(db, importDir, filepath.Join(dir, "images")),
		PollInterval: 10 * time.Millisecond,
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(func() { srv.Close(); cancel(); runner.Wait(); db.Close() })
	code, err := authStore.Bootstrap(context.Background(), "onat", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return &harness{t: t, server: srv.URL, code: code, importDir: importDir, config: filepath.Join(dir, "config", "config.json"), worker: w, runner: runner}
}

func (h *harness) run(ctx context.Context, stdin string, interactive bool, args ...string) (int, string, string) {
	h.t.Helper()
	var stdout, stderr bytes.Buffer
	c := &cli{
		stdin:       bufio.NewReader(strings.NewReader(stdin)),
		stdout:      &stdout,
		stderr:      &stderr,
		interactive: interactive,
		configPath:  h.config,
		newKey:      randomKey,
	}
	code := c.run(ctx, args)
	return code, stdout.String(), stderr.String()
}

func (h *harness) ok(args ...string) string {
	h.t.Helper()
	code, out, errOut := h.run(context.Background(), "", false, args...)
	if code != exitOK {
		h.t.Fatalf("boxfacectl %s: exit %d\nstdout: %s\nstderr: %s", strings.Join(args, " "), code, out, errOut)
	}
	return out
}

func (h *harness) loginAndImport() string {
	h.t.Helper()
	h.ok("login", "--server", h.server, "--code", h.code)
	os.MkdirAll(filepath.Join(h.importDir, "base"), 0o755)
	os.WriteFile(filepath.Join(h.importDir, "base", "vmlinux"), []byte("kernel"), 0o644)
	os.WriteFile(filepath.Join(h.importDir, "base", "rootfs.ext4"), []byte("rootfs"), 0o644)
	var img images.Image
	json.Unmarshal([]byte(h.ok("--json", "image", "import", "base")), &img)
	if img.ID == "" {
		h.t.Fatal("image import returned no ID")
	}
	return img.ID
}

func TestLifecycleCommands(t *testing.T) {
	h := newHarness(t)
	image := h.loginAndImport()

	info, err := os.Stat(h.config)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("config = %v, %v", info, err)
	}

	var created lifecycle.Environment
	if err := json.Unmarshal([]byte(h.ok("--json", "create", "travel", "--image", image, "--vcpus", "1", "--memory-mib", "512", "--disk-gib", "2")), &created); err != nil {
		t.Fatal(err)
	}
	if created.Name != "travel" || created.ObservedState != "running" {
		t.Fatalf("created = %+v", created)
	}
	if out := h.ok("list"); !strings.Contains(out, "travel") || !strings.Contains(out, "running") {
		t.Fatalf("list = %s", out)
	}
	if out := h.ok("inspect", "travel"); !strings.Contains(out, created.ID) {
		t.Fatalf("inspect = %s", out)
	}
	if out := h.ok("stop", "travel"); !strings.Contains(out, "stopped") {
		t.Fatalf("stop = %s", out)
	}
	if out := h.ok("start", "travel"); !strings.Contains(out, "running") {
		t.Fatalf("start = %s", out)
	}

	code, _, errOut := h.run(context.Background(), "", false, "delete", "travel")
	if code != exitError || !strings.Contains(errOut, "--yes") {
		t.Fatalf("delete without confirmation: exit %d, %s", code, errOut)
	}
	code, _, errOut = h.run(context.Background(), "wrong\n", true, "delete", "travel")
	if code != exitError || !strings.Contains(errOut, "nothing deleted") {
		t.Fatalf("delete with wrong name: exit %d, %s", code, errOut)
	}
	if out := h.ok("inspect", "travel"); !strings.Contains(out, "running") {
		t.Fatalf("environment changed after refused deletes: %s", out)
	}
	code, out, errOut := h.run(context.Background(), "travel\n", true, "delete", "travel")
	if code != exitOK || !strings.Contains(out, "deleted") || !strings.Contains(out, "retained") {
		t.Fatalf("confirmed delete: exit %d\n%s\n%s", code, out, errOut)
	}
	if out := h.ok("delete", "travel", "--purge-data", "--yes"); !strings.Contains(out, "purged") {
		t.Fatalf("purge = %s", out)
	}
}

func TestInterruptedFollowLeavesOperationRunning(t *testing.T) {
	h := newHarness(t)
	image := h.loginAndImport()
	h.worker.gate = make(chan struct{})
	h.worker.entered = make(chan struct{}, 1)

	ctx, cancel := context.WithCancel(context.Background())
	go func() { <-h.worker.entered; cancel() }()
	code, _, errOut := h.run(ctx, "", false, "create", "travel", "--image", image)
	if code != exitInterrupted || !strings.Contains(errOut, "continues on the server") {
		t.Fatalf("interrupted create: exit %d, %s", code, errOut)
	}
	opID := strings.Fields(strings.SplitN(errOut, "operation ", 2)[1])[0]

	close(h.worker.gate)
	if out := h.ok("operation", opID, "--follow"); !strings.Contains(out, "running") {
		t.Fatalf("follow after interrupt = %s", out)
	}
}

func TestUsageAndLoginErrors(t *testing.T) {
	h := newHarness(t)
	for _, args := range [][]string{{}, {"bogus"}, {"create"}, {"create", "x"}, {"image", "import"}} {
		if code, _, _ := h.run(context.Background(), "", false, args...); code != exitUsage {
			t.Fatalf("%v: exit %d, want usage", args, code)
		}
	}
	if code, _, errOut := h.run(context.Background(), "", false, "list"); code != exitError || !strings.Contains(errOut, "not logged in") {
		t.Fatalf("list before login: exit %d, %s", code, errOut)
	}
	if code, _, errOut := h.run(context.Background(), "", false, "login", "--server", h.server, "--code", "WRONG"); code != exitError || !strings.Contains(errOut, "unauthorized") {
		t.Fatalf("bad code: exit %d, %s", code, errOut)
	}
	if code, _, _ := h.run(context.Background(), h.code+"\n", true, "login", "--server", h.server); code != exitOK {
		t.Fatalf("interactive login: exit %d", code)
	}
}
