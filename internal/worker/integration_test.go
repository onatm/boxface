//go:build integration && linux

package worker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/onatm/boxface/internal/lifecycle"
)

const itImageID = "it-image"

func integrationConfig(t *testing.T) Config {
	t.Helper()
	if os.Getuid() != 0 {
		t.Skip("integration tests need root")
	}
	image := os.Getenv("BOXFACE_IT_IMAGE")
	if image == "" {
		t.Skip("BOXFACE_IT_IMAGE is not set to a directory with vmlinux and rootfs.ext4")
	}
	if _, err := exec.Command("id", "boxface-vm").Output(); err != nil {
		if out, err := exec.Command("useradd", "--system", "--no-create-home", "--shell", "/usr/sbin/nologin", "boxface-vm").CombinedOutput(); err != nil {
			t.Fatalf("create boxface-vm user: %v: %s", err, out)
		}
	}
	state, err := os.MkdirTemp("/var/tmp", "boxface-it-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(state) })
	images := filepath.Join(state, "images")
	for _, dir := range []string{state, images} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		os.Chmod(dir, 0o755)
	}
	if err := os.Symlink(image, filepath.Join(images, itImageID)); err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(state, "key")
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", key).CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen: %v: %s", err, out)
	}
	firecracker := os.Getenv("BOXFACE_IT_FIRECRACKER")
	if firecracker == "" {
		firecracker = "/usr/local/bin/firecracker"
	}
	return Config{
		StateDir:     state,
		ImagesDir:    images,
		Firecracker:  firecracker,
		VMUser:       "boxface-vm",
		Pool:         netip.MustParsePrefix("172.20.0.0/16"),
		ReadyTimeout: 2 * time.Minute,
		StopTimeout:  30 * time.Second,
	}
}

func guestSSH(cfg Config, guest netip.Addr, command string) (string, error) {
	out, err := exec.Command("ssh", "-i", filepath.Join(cfg.StateDir, "key"),
		"-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null", "-o", "ConnectTimeout=10", "-o", "LogLevel=ERROR",
		"dev@"+guest.String(), command).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

func dropped(cfg Config, guest, target netip.Addr) error {
	out, err := guestSSH(cfg, guest, fmt.Sprintf("timeout 5 bash -c '</dev/tcp/%s/22' 2>/dev/null; echo rc=$?", target))
	if err != nil {
		return err
	}
	if out != "rc=124" {
		return fmt.Errorf("connection to %s:22 was not dropped: %s", target, out)
	}
	return nil
}

func findEnv(inv Inventory, id string) (EnvironmentResources, bool) {
	for _, e := range inv.Environments {
		if e.EnvironmentID == id {
			return e, true
		}
	}
	return EnvironmentResources{}, false
}

func TestIntegrationLifecycle(t *testing.T) {
	cfg := integrationConfig(t)
	ctx := context.Background()

	suffix := make([]byte, 4)
	rand.Read(suffix)
	ids := []string{"it-a-" + hex.EncodeToString(suffix), "it-b-" + hex.EncodeToString(suffix)}
	t.Cleanup(func() {
		for _, id := range ids {
			exec.Command("systemctl", "stop", unitName(id)).Run()
			exec.Command("ip", "link", "del", "dev", tapName(id)).Run()
		}
	})

	h, err := New(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	publicKey, err := os.ReadFile(filepath.Join(cfg.StateDir, "key.pub"))
	if err != nil {
		t.Fatal(err)
	}
	specs := map[string]lifecycle.VMSpec{}
	for _, id := range ids {
		disk := lifecycle.DiskSpec{DiskID: id + "-disk", EnvironmentID: id, SizeGiB: 4, AuthorizedKeys: []string{strings.TrimSpace(string(publicKey))}}
		spec := lifecycle.VMSpec{EnvironmentID: id, DiskID: disk.DiskID, ImageID: itImageID, VCPUs: 1, MemoryMiB: 1024}
		specs[id] = spec
		started := time.Now()
		for i := 0; i < 2; i++ {
			for name, err := range map[string]error{
				"CreateDisk":    h.CreateDisk(ctx, disk),
				"CreateNetwork": h.CreateNetwork(ctx, id),
			} {
				if err != nil {
					t.Fatalf("%s %s (call %d): %v", name, id, i+1, err)
				}
			}
			if err := h.StartVM(ctx, spec); err != nil {
				t.Fatalf("StartVM %s (call %d): %v", id, i+1, err)
			}
		}
		if err := h.WaitReady(ctx, id); err != nil {
			t.Fatalf("WaitReady %s: %v", id, err)
		}
		t.Logf("%s: create to ready in %s", id, time.Since(started).Round(time.Millisecond))
	}

	inv, err := h.Inventory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if e, ok := findEnv(inv, id); !ok || !e.Network || !e.VM || !e.Ready {
			t.Fatalf("inventory for %s = %+v (found %v)", id, e, ok)
		}
	}

	a, err := h.readAddressing(ids[0])
	if err != nil {
		t.Fatal(err)
	}
	b, err := h.readAddressing(ids[1])
	if err != nil {
		t.Fatal(err)
	}
	if a.Guest == b.Guest {
		t.Fatalf("both guests got %s", a.Guest)
	}
	if _, err := guestSSH(cfg, a.Guest, "curl -fsS -m 15 -o /dev/null https://deb.debian.org"); err != nil {
		t.Fatalf("guest cannot reach the internet: %v", err)
	}
	if err := dropped(cfg, a.Guest, a.Host); err != nil {
		t.Fatalf("guest to host: %v", err)
	}
	if err := dropped(cfg, a.Guest, b.Guest); err != nil {
		t.Fatalf("guest to guest: %v", err)
	}
	if _, err := guestSSH(cfg, b.Guest, "true"); err != nil {
		t.Fatalf("second guest is not reachable from the host, so the guest-to-guest check proves nothing: %v", err)
	}

	h.Close()
	restarted := time.Now()
	h, err = New(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	for _, id := range ids {
		if err := h.WaitReady(ctx, id); err != nil {
			t.Fatalf("after worker restart, WaitReady %s: %v", id, err)
		}
	}
	t.Logf("worker restart to rediscovered in %s", time.Since(restarted).Round(time.Millisecond))

	for _, id := range ids {
		stopped := time.Now()
		for i := 0; i < 2; i++ {
			if err := h.StopVM(ctx, id); err != nil {
				t.Fatalf("StopVM %s (call %d): %v", id, i+1, err)
			}
		}
		t.Logf("%s: stopped in %s", id, time.Since(stopped).Round(time.Millisecond))
		for i := 0; i < 2; i++ {
			if err := h.RemoveNetwork(ctx, id); err != nil {
				t.Fatalf("RemoveNetwork %s (call %d): %v", id, i+1, err)
			}
			if err := h.RemoveDisk(ctx, specs[id].DiskID); err != nil {
				t.Fatalf("RemoveDisk %s (call %d): %v", id, i+1, err)
			}
		}
	}

	inv, err = h.Inventory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if e, ok := findEnv(inv, id); ok {
			t.Fatalf("resources left for %s: %+v", id, e)
		}
		if linkExists(ctx, tapName(id)) {
			t.Fatalf("tap left for %s", id)
		}
	}
	if len(inv.Disks) != 0 {
		t.Fatalf("disks left: %v", inv.Disks)
	}
}

func TestIntegrationPeerCredentials(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("integration tests need root")
	}
	for _, tc := range []struct {
		name  string
		allow func(uint32) bool
		ok    bool
	}{
		{"root allowed", func(uid uint32) bool { return uid == 0 }, true},
		{"nobody allowed", func(uint32) bool { return false }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			socket := filepath.Join(t.TempDir(), "w.sock")
			l, err := net.Listen("unix", socket)
			if err != nil {
				t.Fatal(err)
			}
			srv := &http.Server{Handler: NewHandler(&recordingBackend{})}
			go srv.Serve(PeerListener{Listener: l, Allow: tc.allow})
			defer srv.Close()
			_, err = NewClient(socket).Inventory(context.Background())
			if (err == nil) != tc.ok {
				t.Fatalf("inventory err = %v, want success %v", err, tc.ok)
			}
		})
	}
}
