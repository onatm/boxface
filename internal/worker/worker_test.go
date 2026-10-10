package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
