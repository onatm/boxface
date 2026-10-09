package worker

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/onatm/boxface/internal/lifecycle"
)

func testHost(t *testing.T) *Host {
	t.Helper()
	state := t.TempDir()
	h := &Host{
		cfg:       Config{StateDir: state, ImagesDir: "/images", Pool: netip.MustParsePrefix("172.20.0.0/16")},
		vmUID:     os.Getuid(),
		vmGID:     os.Getgid(),
		listeners: map[string]net.Listener{},
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
	if len(cfg.Drives) != 2 || !cfg.Drives[0].Root || !cfg.Drives[0].ReadOnly || cfg.Drives[1].ReadOnly || cfg.Drives[1].Path != h.diskPath("disk-a") {
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
		`ip saddr 172.20.0.0/16 oifname != "bfx*" masquerade`,
	} {
		if !strings.Contains(rules, want) {
			t.Fatalf("ruleset missing %q:\n%s", want, rules)
		}
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
