package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/onatm/boxface/internal/api"
	"github.com/onatm/boxface/internal/auth"
	"github.com/onatm/boxface/internal/images"
	"github.com/onatm/boxface/internal/lifecycle"
	"github.com/onatm/boxface/internal/sshkeys"
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

type fakeGateway struct{}

func (fakeGateway) Endpoint(context.Context, string) (netip.Addr, error) {
	return netip.MustParseAddr("172.20.0.6"), nil
}
func (fakeGateway) HostKey(context.Context, string) (string, error) {
	return "ssh-ed25519 AAAAhost", nil
}

type harness struct {
	t         *testing.T
	server    string
	tailnet   string
	authStore *auth.Store
	code      string
	importDir string
	config    string
	worker    *gatedWorker
	runner    *lifecycle.Runner
	sshCalls  [][]string
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
		SSHKeys:      sshkeys.NewStore(db),
		Gateway:      fakeGateway{},
		PollInterval: 10 * time.Millisecond,
		DialGuest: func(context.Context, string) (net.Conn, error) {
			guest, client := net.Pipe()
			go func() { io.Copy(guest, guest); guest.Close() }()
			return client, nil
		},
	}
	h := s.Handler()
	srv := httptest.NewServer(h)
	tailnet := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Set("Tailscale-User-Login", "onat@github")
		h.ServeHTTP(w, r)
	}))
	tailnet.Config.ConnContext = api.ViaTailscale
	tailnet.Start()
	t.Cleanup(func() { srv.Close(); tailnet.Close(); cancel(); runner.Wait(); db.Close() })
	code, err := authStore.Bootstrap(context.Background(), "onat", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return &harness{t: t, server: srv.URL, tailnet: tailnet.URL, authStore: authStore, code: code, importDir: importDir, config: filepath.Join(dir, "Application Support", "config.json"), worker: w, runner: runner}
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
		ssh: func(args, env []string) error {
			h.sshCalls = append(h.sshCalls, append(args, env...))
			return nil
		},
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
	for _, args := range [][]string{{}, {"bogus"}, {"create"}, {"create", "x", "y"}, {"image", "import"}, {"login", "a", "b"}} {
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

func TestLoginRegistersDeviceKeyAndCreateUsesDefaults(t *testing.T) {
	h := newHarness(t)
	code, _, errOut := h.run(context.Background(), "", false, "login", h.server, "--code", h.code)
	if code != exitOK || !strings.Contains(errOut, "registered SSH key SHA256:") {
		t.Fatalf("login: exit %d, %s", code, errOut)
	}
	keyPath := filepath.Join(filepath.Dir(h.config), "id_ed25519")
	if info, err := os.Stat(keyPath); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("device key = %v, %v", info, err)
	}
	if code, _, errOut := h.run(context.Background(), "", false, "login", h.server, "--code", h.code); code != exitError || !strings.Contains(errOut, "unauthorized") {
		t.Fatalf("reused code: exit %d, %s", code, errOut)
	}

	os.MkdirAll(filepath.Join(h.importDir, "base"), 0o755)
	os.WriteFile(filepath.Join(h.importDir, "base", "vmlinux"), []byte("kernel"), 0o644)
	os.WriteFile(filepath.Join(h.importDir, "base", "rootfs.ext4"), []byte("rootfs"), 0o644)
	h.ok("image", "import", "base", "--alias", "main")
	if out := h.ok("image", "list"); !strings.Contains(out, "main") {
		t.Fatalf("image list = %s", out)
	}
	var created lifecycle.Environment
	if err := json.Unmarshal([]byte(h.ok("--json", "create", "travel")), &created); err != nil {
		t.Fatal(err)
	}
	if created.ObservedState != "running" || created.VCPUs != 2 || created.MemoryMiB != 2048 {
		t.Fatalf("created = %+v", created)
	}
}

func (h *harness) createMain() lifecycle.Environment {
	h.t.Helper()
	h.ok("login", h.server, "--code", h.code)
	os.MkdirAll(filepath.Join(h.importDir, "base"), 0o755)
	os.WriteFile(filepath.Join(h.importDir, "base", "vmlinux"), []byte("kernel"), 0o644)
	os.WriteFile(filepath.Join(h.importDir, "base", "rootfs.ext4"), []byte("rootfs"), 0o644)
	h.ok("image", "import", "base", "--alias", "main")
	var env lifecycle.Environment
	if err := json.Unmarshal([]byte(h.ok("--json", "create", "travel")), &env); err != nil {
		h.t.Fatal(err)
	}
	return env
}

func TestConnectStartsStoppedEnvironmentAndPinsHostKey(t *testing.T) {
	h := newHarness(t)
	env := h.createMain()
	h.ok("stop", "travel")

	code, _, errOut := h.run(context.Background(), "", false, "connect", "travel")
	if code != exitOK || !strings.Contains(errOut, "travel is stopped; starting it") {
		t.Fatalf("connect: exit %d, %s", code, errOut)
	}
	if out := h.ok("inspect", "travel"); !strings.Contains(out, "running") {
		t.Fatalf("not started: %s", out)
	}
	if len(h.sshCalls) != 1 {
		t.Fatalf("ssh calls = %v", h.sshCalls)
	}
	dir := filepath.Dir(h.config)
	args := strings.Join(h.sshCalls[0], " ")
	for _, want := range []string{
		"-i " + filepath.Join(dir, "id_ed25519"),
		`UserKnownHostsFile="` + filepath.Join(dir, "known_hosts") + `"`,
		"StrictHostKeyChecking=yes",
		"HostKeyAlias=" + env.ID,
		" tunnel " + env.ID,
		"ServerAliveInterval=15",
		"-t dev@travel tmux new-session -A -s main",
		"BOXFACE_CONFIG=" + h.config,
	} {
		if !strings.Contains(args, want) {
			t.Fatalf("ssh args %q lack %q", args, want)
		}
	}

	os.WriteFile(filepath.Join(dir, "known_hosts"), []byte("other ssh-ed25519 AAAAother\n"+env.ID+" ssh-ed25519 AAAAstale\n"), 0o600)
	h.ok("connect", "travel", "--session", "work")
	if args := strings.Join(h.sshCalls[1], " "); !strings.Contains(args, "-s work") {
		t.Fatalf("session args = %s", args)
	}
	known, _ := os.ReadFile(filepath.Join(dir, "known_hosts"))
	if string(known) != "other ssh-ed25519 AAAAother\n"+env.ID+" ssh-ed25519 AAAAhost\n" {
		t.Fatalf("known_hosts = %q", known)
	}
	if code, _, _ := h.run(context.Background(), "", false, "connect", "travel", "--session", "a b"); code != exitUsage {
		t.Fatalf("bad session name: exit %d", code)
	}
}

func TestInterruptedConnectLeavesStartRunning(t *testing.T) {
	h := newHarness(t)
	h.createMain()
	h.ok("stop", "travel")
	h.worker.gate = make(chan struct{})
	h.worker.entered = make(chan struct{}, 1)

	ctx, cancel := context.WithCancel(context.Background())
	go func() { <-h.worker.entered; cancel() }()
	code, _, errOut := h.run(ctx, "", false, "connect", "travel")
	if code != exitInterrupted || !strings.Contains(errOut, "continues on the server") || len(h.sshCalls) != 0 {
		t.Fatalf("interrupted connect: exit %d, ssh %v, %s", code, h.sshCalls, errOut)
	}
	close(h.worker.gate)
	h.runner.Wait()
	if out := h.ok("inspect", "travel"); !strings.Contains(out, "running") {
		t.Fatalf("start did not finish: %s", out)
	}
}

func TestTunnelCarriesBytes(t *testing.T) {
	h := newHarness(t)
	env := h.createMain()

	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	c := &cli{stdin: bufio.NewReader(stdinR), stdout: stdoutW, stderr: io.Discard, configPath: h.config}
	done := make(chan int)
	go func() { done <- c.run(context.Background(), []string{"tunnel", env.ID}) }()
	io.WriteString(stdinW, "SSH-2.0-test\r\n")
	line, err := bufio.NewReader(stdoutR).ReadString('\n')
	if err != nil || line != "SSH-2.0-test\r\n" {
		t.Fatalf("echo = %q, %v", line, err)
	}
	stdinW.Close()
	go io.Copy(io.Discard, stdoutR)
	if code := <-done; code != exitOK {
		t.Fatalf("tunnel exit %d", code)
	}

	h.ok("stop", "travel")
	code, _, errOut := h.run(context.Background(), "", false, "tunnel", env.ID)
	if code != exitError || !strings.Contains(errOut, "environment_not_running") {
		t.Fatalf("tunnel to stopped: exit %d, %s", code, errOut)
	}
}

func TestLoginWithTailscaleIdentityNeedsNoCode(t *testing.T) {
	h := newHarness(t)
	code, _, errOut := h.run(context.Background(), "", false, "login", h.tailnet)
	if code != exitError || !strings.Contains(errOut, "login code required") {
		t.Fatalf("unbound identity: exit %d, %s", code, errOut)
	}
	if _, err := h.authStore.BindOwnerIdentity(context.Background(), "onat", "tailscale", "onat@github"); err != nil {
		t.Fatal(err)
	}
	code, _, errOut = h.run(context.Background(), "", false, "login", h.tailnet)
	if code != exitOK || !strings.Contains(errOut, "signed in with your Tailscale login") || !strings.Contains(errOut, "registered SSH key") {
		t.Fatalf("identity login: exit %d, %s", code, errOut)
	}
	h.ok("list")
	if code, _, errOut := h.run(context.Background(), h.code+"\n", true, "login", h.server); code != exitOK || !strings.Contains(errOut, "logged in") {
		t.Fatalf("code login on a plain listener: exit %d, %s", code, errOut)
	}
}
