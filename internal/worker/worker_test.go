package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/onatm/boxface/internal/lifecycle"
)

func testHost(t *testing.T) *Host {
	t.Helper()
	state := t.TempDir()
	h := &Host{
		cfg:       Config{StateDir: state, ImagesDir: "/images", Pool: netip.MustParsePrefix("172.20.0.0/16")},
		vmUID:     os.Getuid(),
		vmGID:     os.Getgid(),
		listeners: map[string][]net.Listener{},
		broker:    newBroker(200 * time.Millisecond),
	}
	if err := os.MkdirAll(h.environmentsDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	return h
}

func TestTapNameFitsInterfaceLimit(t *testing.T) {
	name := tapName("95a1097f-784e-4aea-9992-0bb76259ab9b")
	if name != "bfx95a1097f784e" || len(name) > 15 {
		t.Fatalf("tap name = %q", name)
	}
}

func TestAddressing(t *testing.T) {
	h := testHost(t)
	cases := map[int][2]string{0: {"172.20.0.1", "172.20.0.2"}, 1: {"172.20.0.5", "172.20.0.6"}, 64: {"172.20.1.1", "172.20.1.2"}}
	for index, want := range cases {
		a, err := h.addressing(index)
		if err != nil {
			t.Fatal(err)
		}
		if a.Host.String() != want[0] || a.Guest.String() != want[1] {
			t.Fatalf("index %d = %s/%s, want %v", index, a.Host, a.Guest, want)
		}
	}
	if _, err := h.addressing(16384); err == nil {
		t.Fatal("index beyond a /16 pool accepted")
	}
}

func TestAllocateReusesAndFillsGaps(t *testing.T) {
	h := testHost(t)
	a, _ := h.allocate("env-a")
	b, _ := h.allocate("env-b")
	again, _ := h.allocate("env-a")
	if a.Index != 0 || b.Index != 1 || again != a {
		t.Fatalf("a=%+v b=%+v again=%+v", a, b, again)
	}
	if err := os.RemoveAll(h.envDir("env-a")); err != nil {
		t.Fatal(err)
	}
	c, _ := h.allocate("env-c")
	if c.Index != 0 {
		t.Fatalf("freed slot not reused: %+v", c)
	}
}

func TestFirecrackerConfig(t *testing.T) {
	h := testHost(t)
	a, _ := h.addressing(1)
	b, err := h.firecrackerConfig(lifecycle.VMSpec{EnvironmentID: "env-a", DiskID: "disk-a", ImageID: "image-1", VCPUs: 2, MemoryMiB: 1024}, a)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		BootSource struct {
			Kernel   string `json:"kernel_image_path"`
			BootArgs string `json:"boot_args"`
		} `json:"boot-source"`
		Drives []struct {
			Path     string `json:"path_on_host"`
			Root     bool   `json:"is_root_device"`
			ReadOnly bool   `json:"is_read_only"`
		} `json:"drives"`
		Network []struct {
			MAC string `json:"guest_mac"`
			Tap string `json:"host_dev_name"`
		} `json:"network-interfaces"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(cfg.BootSource.BootArgs, "ip=172.20.0.6::172.20.0.5:255.255.255.252::eth0:off") {
		t.Fatalf("boot args = %q", cfg.BootSource.BootArgs)
	}
	if cfg.BootSource.Kernel != "/images/image-1/vmlinux" {
		t.Fatalf("kernel = %q", cfg.BootSource.Kernel)
	}
	if len(cfg.Drives) != 3 || !cfg.Drives[0].Root || !cfg.Drives[0].ReadOnly || cfg.Drives[1].ReadOnly || cfg.Drives[1].Path != h.diskPath("disk-a") ||
		cfg.Drives[2].Root || !cfg.Drives[2].ReadOnly || cfg.Drives[2].Path != filepath.Join(h.envDir("env-a"), keysImage) {
		t.Fatalf("drives = %+v", cfg.Drives)
	}
	if cfg.Network[0].MAC != "06:00:ac:14:00:06" || cfg.Network[0].Tap != tapName("env-a") {
		t.Fatalf("network = %+v", cfg.Network)
	}
}

func TestFirecrackerConfigAttachesProfileDrive(t *testing.T) {
	h := testHost(t)
	a, _ := h.addressing(1)
	b, err := h.firecrackerConfig(lifecycle.VMSpec{EnvironmentID: "env-a", DiskID: "disk-a", ImageID: "base", VCPUs: 1, MemoryMiB: 512, ProfileID: "rev-1"}, a)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Drives []struct {
			ID       string `json:"drive_id"`
			Path     string `json:"path_on_host"`
			Root     bool   `json:"is_root_device"`
			ReadOnly bool   `json:"is_read_only"`
		} `json:"drives"`
	}
	json.Unmarshal(b, &cfg)
	if len(cfg.Drives) != 4 || cfg.Drives[3].ID != "profile" || cfg.Drives[3].Path != filepath.Join(h.cfg.StateDir, "profiles", "rev-1.img") || !cfg.Drives[3].ReadOnly || cfg.Drives[3].Root {
		t.Fatalf("drives = %+v", cfg.Drives)
	}
	if err := h.StartVM(context.Background(), lifecycle.VMSpec{EnvironmentID: "env-a", DiskID: "disk-a", ImageID: "base", ProfileID: "../escape"}); err == nil || !strings.Contains(err.Error(), "invalid identifier") {
		t.Fatalf("profile ID with a path: err = %v", err)
	}
}

func TestFirewallRuleset(t *testing.T) {
	rules := testHost(t).firewallRuleset()
	for _, want := range []string{
		`iifname "bfx*" ct state established,related accept`,
		`iifname "bfx*" drop`,
		`iifname "bfx*" oifname "bfx*" drop`,
		`iifname "bfx*" ip daddr { 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 100.64.0.0/10, 169.254.0.0/16 } drop`,
		`iifname "bfx*" ip6 daddr fd7a:115c:a1e0::/48 drop`,
		`ip saddr 172.20.0.0/16 oifname != "bfx*" masquerade`,
	} {
		if !strings.Contains(rules, want) {
			t.Fatalf("ruleset missing %q:\n%s", want, rules)
		}
	}
	if strings.Index(rules, "100.64.0.0/10") > strings.Index(rules, `iifname "bfx*" accept`) {
		t.Fatalf("private and tailnet drops must precede the guest accept:\n%s", rules)
	}
}

func TestValidIDRejectsPathCharacters(t *testing.T) {
	for _, id := range []string{"", "../etc", "a/b", "A", "x y"} {
		if validID(id) == nil {
			t.Fatalf("validID(%q) accepted", id)
		}
	}
	if err := validID("95a1097f-784e-4aea-9992-0bb76259ab9b"); err != nil {
		t.Fatal(err)
	}
}

type recordingBackend struct {
	calls []string
	fail  error
}

func (r *recordingBackend) record(call string) error {
	r.calls = append(r.calls, call)
	return r.fail
}
func (r *recordingBackend) CreateDisk(_ context.Context, s lifecycle.DiskSpec) error {
	return r.record("CreateDisk " + s.DiskID)
}
func (r *recordingBackend) RemoveDisk(_ context.Context, id string) error {
	return r.record("RemoveDisk " + id)
}
func (r *recordingBackend) CreateNetwork(_ context.Context, id string) error {
	return r.record("CreateNetwork " + id)
}
func (r *recordingBackend) RemoveNetwork(_ context.Context, id string) error {
	return r.record("RemoveNetwork " + id)
}
func (r *recordingBackend) StartVM(_ context.Context, s lifecycle.VMSpec) error {
	return r.record("StartVM " + s.EnvironmentID)
}
func (r *recordingBackend) StopVM(_ context.Context, id string) error {
	return r.record("StopVM " + id)
}
func (r *recordingBackend) WaitReady(_ context.Context, id string) error {
	return r.record("WaitReady " + id)
}
func (r *recordingBackend) Inventory(context.Context) (Inventory, error) {
	return Inventory{Environments: []EnvironmentResources{{EnvironmentID: "env-a", VM: true}}, Disks: []string{"disk-a"}}, r.fail
}

func (r *recordingBackend) Endpoint(_ context.Context, id string) (netip.Addr, error) {
	return netip.MustParseAddr("172.20.0.6"), r.record("Endpoint " + id)
}
func (r *recordingBackend) SetCredentials(_ context.Context, id string, creds lifecycle.Credentials) error {
	return r.record(fmt.Sprintf("SetCredentials %s %d", id, len(creds)))
}
func (r *recordingBackend) HostKey(_ context.Context, id string) (string, error) {
	return "ssh-ed25519 AAAA", r.record("HostKey " + id)
}

func (r *recordingBackend) StartBuild(_ context.Context, s lifecycle.BuildSpec) error {
	return r.record("StartBuild " + s.BuildID)
}
func (r *recordingBackend) BuildStatus(_ context.Context, id string) (lifecycle.BuildStatus, error) {
	return lifecycle.BuildStatus{State: "running"}, r.record("BuildStatus " + id)
}

func (r *recordingBackend) Storage(context.Context) (Storage, error) {
	return Storage{FreeBytes: 5 << 30}, r.record("Storage")
}
func (r *recordingBackend) ProfileDrives(context.Context) ([]string, error) {
	return []string{"key-a"}, r.record("ProfileDrives")
}
func (r *recordingBackend) RemoveProfileDrive(_ context.Context, key string) error {
	return r.record("RemoveProfileDrive " + key)
}
func (r *recordingBackend) RemoveBuildCache(_ context.Context, owner string) error {
	return r.record("RemoveBuildCache " + owner)
}

func serve(t *testing.T, b Backend) *Client {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "bfx")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "w.sock")
	l, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: NewHandler(b)}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })
	return NewClient(socket)
}

func TestClientRoundTrip(t *testing.T) {
	b := &recordingBackend{}
	c := serve(t, b)
	ctx := context.Background()
	var w lifecycle.Worker = c
	steps := []error{
		w.CreateDisk(ctx, lifecycle.DiskSpec{DiskID: "disk-a"}),
		w.CreateNetwork(ctx, "env-a"),
		w.StartVM(ctx, lifecycle.VMSpec{EnvironmentID: "env-a"}),
		w.WaitReady(ctx, "env-a"),
		w.StopVM(ctx, "env-a"),
		w.RemoveNetwork(ctx, "env-a"),
		w.RemoveDisk(ctx, "disk-a"),
	}
	for i, err := range steps {
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	want := "CreateDisk disk-a,CreateNetwork env-a,StartVM env-a,WaitReady env-a,StopVM env-a,RemoveNetwork env-a,RemoveDisk disk-a"
	if got := strings.Join(b.calls, ","); got != want {
		t.Fatalf("calls = %s", got)
	}
	inv, err := c.Inventory(ctx)
	if err != nil || len(inv.Environments) != 1 || !inv.Environments[0].VM || inv.Disks[0] != "disk-a" {
		t.Fatalf("inventory = %+v, %v", inv, err)
	}
	if addr, err := c.Endpoint(ctx, "env-a"); err != nil || addr != netip.MustParseAddr("172.20.0.6") {
		t.Fatalf("endpoint = %v, %v", addr, err)
	}
	if key, err := c.HostKey(ctx, "disk-a"); err != nil || key != "ssh-ed25519 AAAA" {
		t.Fatalf("host key = %q, %v", key, err)
	}
	if got := strings.Join(b.calls[len(b.calls)-2:], ","); got != "Endpoint env-a,HostKey disk-a" {
		t.Fatalf("calls = %s", got)
	}
	if err := c.SetCredentials(ctx, "env-a", lifecycle.Credentials{"claude-code": {"CLAUDE_CODE_OAUTH_TOKEN": "t"}}); err != nil || b.calls[len(b.calls)-1] != "SetCredentials env-a 1" {
		t.Fatalf("set credentials = %v, calls %v", err, b.calls)
	}
	b.fail = errors.New("environment env-a VM is not running")
	if _, err := c.Endpoint(ctx, "env-a"); err == nil || err.Error() != b.fail.Error() {
		t.Fatalf("endpoint err = %v", err)
	}
}

func TestClientReturnsWorkerError(t *testing.T) {
	c := serve(t, &recordingBackend{fail: errors.New("mkfs.ext4 failed")})
	if err := c.CreateDisk(context.Background(), lifecycle.DiskSpec{DiskID: "disk-a"}); err == nil || err.Error() != "mkfs.ext4 failed" {
		t.Fatalf("err = %v", err)
	}
}

func TestClientReportsUnavailableWorker(t *testing.T) {
	c := NewClient(filepath.Join(t.TempDir(), "missing.sock"))
	if err := c.StopVM(context.Background(), "env-a"); !errors.Is(err, lifecycle.ErrWorkerUnavailable) {
		t.Fatalf("err = %v, want ErrWorkerUnavailable", err)
	}
	if err := serve(t, &recordingBackend{fail: errors.New("boom")}).StopVM(context.Background(), "env-a"); errors.Is(err, lifecycle.ErrWorkerUnavailable) {
		t.Fatal("worker-reported error classified as unavailable")
	}
}

func TestClientActiveVMs(t *testing.T) {
	active, err := serve(t, &recordingBackend{}).ActiveVMs(context.Background())
	if err != nil || !active["env-a"] || len(active) != 1 {
		t.Fatalf("active = %v, %v", active, err)
	}
}

func TestConsoleSummary(t *testing.T) {
	panicked := "[    1.6] VFS: Cannot open root device\n[    1.6] Kernel panic - not syncing: VFS: Unable to mount root fs on unknown-block(254,0)\n[    1.6] Call Trace:\n[    1.6]  kernel_init+0x15/0x1b0"
	if got := consoleSummary(panicked); got != "guest kernel panic: VFS: Unable to mount root fs on unknown-block(254,0)" {
		t.Fatalf("summary = %q", got)
	}
	if got := consoleSummary("a\nb\nc\nd\ne\nf"); got != "b | c | d | e | f" {
		t.Fatalf("summary = %q", got)
	}
}

func TestKeysImage(t *testing.T) {
	b, err := keysImageContent([]string{"ssh-ed25519 AAAAone", "ssh-ed25519 AAAAtwo"})
	if err != nil {
		t.Fatal(err)
	}
	if len(b) != 4096 || string(bytes.TrimRight(b, "\x00")) != "ssh-ed25519 AAAAone\nssh-ed25519 AAAAtwo\n" {
		t.Fatalf("image = %d bytes, %q", len(b), bytes.TrimRight(b, "\x00"))
	}
	for name, keys := range map[string][]string{
		"none":      nil,
		"empty":     {""},
		"multiline": {"ssh-ed25519 AAAA\nssh-ed25519 BBBB"},
		"nul":       {"ssh-ed25519 AAAA\x00"},
	} {
		if _, err := keysImageContent(keys); err == nil {
			t.Fatalf("%s: accepted %q", name, keys)
		}
	}
}

func askBroker(t *testing.T, socket, request string) brokerReply {
	t.Helper()
	conn, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	io.WriteString(conn, request)
	var reply brokerReply
	if err := json.NewDecoder(conn).Decode(&reply); err != nil {
		t.Fatal(err)
	}
	return reply
}

func TestBrokerServesOnlyTheSocketsEnvironment(t *testing.T) {
	state, err := os.MkdirTemp("/tmp", "bfx")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(state) })
	h := testHost(t)
	h.cfg.StateDir = state
	ctx := context.Background()
	for _, id := range []string{"env-a", "env-b"} {
		os.MkdirAll(h.envDir(id), 0o755)
		if err := h.listen(id); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(h.Close)
	socket := func(id string) string { return filepath.Join(h.envDir(id), "vsock.sock_1025") }

	if r := askBroker(t, socket("env-a"), `{"consumer":"claude-code"}`); !strings.Contains(r.Error, "not loaded") {
		t.Fatalf("before push: %+v", r)
	}
	h.SetCredentials(ctx, "env-a", lifecycle.Credentials{"claude-code": {"CLAUDE_CODE_OAUTH_TOKEN": "token-a"}})
	h.SetCredentials(ctx, "env-b", lifecycle.Credentials{"claude-code": {"CLAUDE_CODE_OAUTH_TOKEN": "token-b"}})
	if r := askBroker(t, socket("env-a"), `{"consumer":"claude-code"}`); r.Env["CLAUDE_CODE_OAUTH_TOKEN"] != "token-a" || len(r.Env) != 1 {
		t.Fatalf("env-a = %+v", r)
	}
	if r := askBroker(t, socket("env-b"), `{"consumer":"claude-code"}`); r.Env["CLAUDE_CODE_OAUTH_TOKEN"] != "token-b" {
		t.Fatalf("env-b = %+v", r)
	}
	if r := askBroker(t, socket("env-a"), `{"consumer":"codex"}`); r.Error != "" || len(r.Env) != 0 {
		t.Fatalf("unbound consumer = %+v", r)
	}
	h.SetCredentials(ctx, "env-a", lifecycle.Credentials{"claude": {"CLAUDE_CODE_OAUTH_TOKEN": "token-a"}, "gh": {"GH_TOKEN": "g"}, "session": {"GIT_TOKEN": "s"}})
	if r := askBroker(t, socket("env-a"), `{"list":true}`); strings.Join(r.Commands, ",") != "claude,gh" || len(r.Env) != 0 {
		t.Fatalf("list = %+v", r)
	}
	if r := askBroker(t, socket("env-a"), `{"consumer":"session"}`); r.Env["GIT_TOKEN"] != "s" || len(r.Env) != 1 {
		t.Fatalf("session = %+v", r)
	}
	if r := askBroker(t, socket("env-a"), `not json`); r.Error == "" {
		t.Fatalf("malformed request = %+v", r)
	}
	h.stopListening("env-a")
	if h.broker.loaded("env-a") || !h.broker.loaded("env-b") {
		t.Fatal("stopping env-a did not clear only its credentials")
	}
}

func TestBrokerWaitsForAPush(t *testing.T) {
	b := newBroker(2 * time.Second)
	go func() {
		time.Sleep(100 * time.Millisecond)
		b.set("env-a", lifecycle.Credentials{"pi": {"OPENAI_API_KEY": "k"}})
	}()
	started := time.Now()
	creds, ok := b.await("env-a")
	if !ok || creds["pi"]["OPENAI_API_KEY"] != "k" || time.Since(started) > time.Second {
		t.Fatalf("await = %v, %v after %s", creds, ok, time.Since(started))
	}
	original := lifecycle.Credentials{"pi": {"A": "1"}}
	b.set("env-b", original)
	original["pi"]["A"] = "changed"
	if got, _ := b.await("env-b"); got["pi"]["A"] != "1" {
		t.Fatal("the broker kept a reference to the caller's map")
	}
}

func TestBrokerNoWait(t *testing.T) {
	b := newBroker(5 * time.Second)
	client, server := net.Pipe()
	go b.serve("env-unloaded", server)
	started := time.Now()
	io.WriteString(client, `{"list":true,"nowait":true}`+"\n")
	var r brokerReply
	if err := json.NewDecoder(client).Decode(&r); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.Error, "not loaded") || time.Since(started) > time.Second {
		t.Fatalf("nowait = %+v after %s", r, time.Since(started))
	}
}

func TestValidateBuild(t *testing.T) {
	good := lifecycle.BuildSpec{BuildID: "0123abcd", OwnerID: "owner-1", Files: map[string]string{"flake.nix": "{}", "flake.lock": "{}"}, VCPUs: 2, MemoryMiB: 2048, TimeoutSec: 3600}
	if err := validateBuild(good); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*lifecycle.BuildSpec){
		"path in build ID":  func(s *lifecycle.BuildSpec) { s.BuildID = "../x" },
		"path in owner":     func(s *lifecycle.BuildSpec) { s.OwnerID = "a/b" },
		"no flake.lock":     func(s *lifecycle.BuildSpec) { delete(s.Files, "flake.lock") },
		"unexpected file":   func(s *lifecycle.BuildSpec) { s.Files["../../etc/passwd"] = "x" },
		"too little memory": func(s *lifecycle.BuildSpec) { s.MemoryMiB = 256 },
		"no timeout":        func(s *lifecycle.BuildSpec) { s.TimeoutSec = 0 },
		"multi-line ref":    func(s *lifecycle.BuildSpec) { s.DotfilesRef = "main\nmalicious" },
		"multi-line repo":   func(s *lifecycle.BuildSpec) { s.DotfilesRepo = "https://a\nb" },
		"no vCPUs":          func(s *lifecycle.BuildSpec) { s.VCPUs = 0 },
		"excessive timeout": func(s *lifecycle.BuildSpec) { s.TimeoutSec = buildTimeoutMax + 1 },
		"excessive memory":  func(s *lifecycle.BuildSpec) { s.MemoryMiB = buildMemoryMax + 1 },
		"empty build ID":    func(s *lifecycle.BuildSpec) { s.BuildID = "" },
		"empty flake.nix":   func(s *lifecycle.BuildSpec) { s.Files["flake.nix"] = "" },
	} {
		s := good
		s.Files = maps.Clone(good.Files)
		mutate(&s)
		if err := validateBuild(s); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
}

func TestCollectBuildResult(t *testing.T) {
	h := testHost(t)
	write := func(id, content string) {
		os.MkdirAll(h.buildDir(id), 0o755)
		b := make([]byte, resultSize)
		copy(b, content)
		os.WriteFile(filepath.Join(h.buildDir(id), "result.img"), b, 0o644)
	}
	write("failed", `{"status":"failed","error":"nix build failed","dotfiles_commit":"abc"}`+"\nerror: undefined variable 'ripgrepp'\n")
	if st := h.collect(context.Background(), "failed"); st.State != "failed" || st.Error != "nix build failed" || !strings.Contains(st.LogTail, "ripgrepp") || st.DotfilesCommit != "abc" {
		t.Fatalf("failed build = %+v", st)
	}
	write("empty", "")
	if st := h.collect(context.Background(), "empty"); st.State != "failed" || !strings.Contains(st.Error, "without a result") {
		t.Fatalf("no result = %+v", st)
	}
	write("lying", `{"status":"ok","home_path":"/nix/store/x","closure_mib":10}`+"\n")
	if st := h.collect(context.Background(), "lying"); st.State != "failed" || !strings.Contains(st.Error, "not valid") {
		t.Fatalf("success without a valid drive = %+v", st)
	}
	if _, err := os.Stat(h.profilePath("lying")); err == nil {
		t.Fatal("published a drive the builder did not produce")
	}
	long := strings.Repeat("line\n", 500)
	write("long", `{"status":"failed"}`+"\n"+long)
	if st := h.collect(context.Background(), "long"); strings.Count(st.LogTail, "\n") != buildLogLines-1 || st.Error != "the build failed" {
		t.Fatalf("long log = %d lines, error %q", strings.Count(st.LogTail, "\n")+1, st.Error)
	}
}

func TestClientBuilds(t *testing.T) {
	b := &recordingBackend{}
	c := serve(t, b)
	ctx := context.Background()
	if err := c.StartBuild(ctx, lifecycle.BuildSpec{BuildID: "key-1"}); err != nil {
		t.Fatal(err)
	}
	if st, err := c.BuildStatus(ctx, "key-1"); err != nil || st.State != "running" {
		t.Fatalf("status = %+v, %v", st, err)
	}
	if got := strings.Join(b.calls, ","); got != "StartBuild key-1,BuildStatus key-1" {
		t.Fatalf("calls = %s", got)
	}
}

func TestStorageAndProfileDrives(t *testing.T) {
	h := testHost(t)
	os.MkdirAll(h.profilesDir(), 0o755)
	if err := sparseFile(h.profilePath("key-a"), 1<<30); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(h.profilePath("key-b"), make([]byte, 1<<20), 0o644)
	os.WriteFile(h.profilePath("key-c")+".tmp", []byte("x"), 0o644)
	os.WriteFile(filepath.Join(h.profilesDir(), "notes.txt"), []byte("x"), 0o644)
	if got := allocated(h.profilesDir()); got < 1<<20 || got > 4<<20 {
		t.Fatalf("allocated = %d; a sparse 1 GiB file must not count its size", got)
	}
	keys, err := h.ProfileDrives(context.Background())
	if err != nil || strings.Join(keys, ",") != "key-a,key-b" {
		t.Fatalf("drives = %v, %v", keys, err)
	}
	if err := h.RemoveProfileDrive(context.Background(), "key-a"); err != nil {
		t.Fatal(err)
	}
	if err := h.RemoveProfileDrive(context.Background(), "../escape"); err == nil {
		t.Fatal("removed a drive named with a path")
	}
	if keys, _ := h.ProfileDrives(context.Background()); strings.Join(keys, ",") != "key-b" {
		t.Fatalf("after remove = %v", keys)
	}
	st, err := h.Storage(context.Background())
	if err != nil || st.TotalBytes == 0 || st.FreeBytes == 0 || st.ProfileDrivesBytes < 1<<20 {
		t.Fatalf("storage = %+v, %v", st, err)
	}
	if err := h.requireFree(st.TotalBytes+1, "test"); err == nil || !strings.Contains(err.Error(), "not enough free disk space") {
		t.Fatalf("requireFree = %v", err)
	}
}

func TestClientStorage(t *testing.T) {
	b := &recordingBackend{}
	c := serve(t, b)
	ctx := context.Background()
	st, err := c.Storage(ctx)
	keys, err2 := c.ProfileDrives(ctx)
	err3 := c.RemoveProfileDrive(ctx, "key-a")
	err4 := c.RemoveBuildCache(ctx, "owner-1")
	if err != nil || err2 != nil || err3 != nil || err4 != nil || st.FreeBytes != 5<<30 || len(keys) != 1 {
		t.Fatalf("storage %+v %v, drives %v %v, remove %v, cache %v", st, err, keys, err2, err3, err4)
	}
	if got := strings.Join(b.calls, ","); got != "Storage,ProfileDrives,RemoveProfileDrive key-a,RemoveBuildCache owner-1" {
		t.Fatalf("calls = %s", got)
	}
}
