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
	"github.com/onatm/boxface/internal/profiles"
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
		StateDir:       state,
		ImagesDir:      images,
		Firecracker:    firecracker,
		VMUser:         "boxface-vm",
		Pool:           netip.MustParsePrefix("172.20.0.0/16"),
		ReadyTimeout:   2 * time.Minute,
		StopTimeout:    30 * time.Second,
		CredentialWait: 2 * time.Second,
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
		disk := lifecycle.DiskSpec{DiskID: id + "-disk", EnvironmentID: id, SizeGiB: 4}
		spec := lifecycle.VMSpec{EnvironmentID: id, DiskID: disk.DiskID, ImageID: itImageID, VCPUs: 1, MemoryMiB: 1024, AuthorizedKeys: []string{strings.TrimSpace(string(publicKey))}}
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
	endpoint, err := h.Endpoint(ctx, ids[0])
	if err != nil || endpoint != a.Guest {
		t.Fatalf("endpoint = %s, %v; want %s", endpoint, err, a.Guest)
	}
	hostKey, err := h.HostKey(ctx, specs[ids[0]].DiskID)
	if err != nil {
		t.Fatal(err)
	}
	knownHosts := filepath.Join(cfg.StateDir, "known_hosts")
	if err := os.WriteFile(knownHosts, []byte(endpoint.String()+" "+hostKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("ssh", "-i", filepath.Join(cfg.StateDir, "key"), "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes",
		"-o", "UserKnownHostsFile="+knownHosts, "-o", "ConnectTimeout=10", "dev@"+endpoint.String(), "true").CombinedOutput(); err != nil {
		t.Fatalf("ssh with the pinned host key: %v: %s", err, out)
	}
	if out, err := guestSSH(cfg, a.Guest, "/usr/local/bin/boxface-run --command codex -- /usr/bin/true; echo rc=$?"); err != nil || !strings.Contains(out, "not loaded yet") || !strings.Contains(out, "rc=1") {
		t.Fatalf("launch before a push = %q, %v", out, err)
	}
	const itValue = "boxface-it-credential-value"
	if err := h.SetCredentials(ctx, ids[0], lifecycle.Credentials{"codex": {"BOXFACE_IT_TOKEN": itValue}}); err != nil {
		t.Fatal(err)
	}
	if out, err := guestSSH(cfg, a.Guest, "/usr/local/bin/boxface-run --command codex -- /usr/bin/printenv BOXFACE_IT_TOKEN"); err != nil || out != itValue {
		t.Fatalf("bound variable in the child = %q, %v", out, err)
	}
	if out, err := guestSSH(cfg, a.Guest, "/usr/local/bin/boxface-run --command pi -- /bin/sh -c 'echo unbound=${BOXFACE_IT_TOKEN:-none}'"); err != nil || out != "unbound=none" {
		t.Fatalf("unbound consumer = %q, %v", out, err)
	}
	if out, err := guestSSH(cfg, a.Guest, "echo parent=${BOXFACE_IT_TOKEN:-none}; grep -rlsF "+itValue+" /run /tmp /home/dev /workspace /mnt/data 2>/dev/null | head -3; echo scanned"); err != nil || out != "parent=none\nscanned" {
		t.Fatalf("value outside the child process: %q, %v", out, err)
	}
	if err := h.SetCredentials(ctx, ids[0], lifecycle.Credentials{"codex": {"BOXFACE_IT_TOKEN": itValue}, "session": {"BOXFACE_IT_SESSION": "session-value"}}); err != nil {
		t.Fatal(err)
	}
	if out, err := guestSSH(cfg, a.Guest, "bash -lc 'echo session=$BOXFACE_IT_SESSION; command -v codex; codex_shim=$(command -v codex); grep -c boxface-run \"$codex_shim\"; echo plain=${BOXFACE_IT_TOKEN:-none}'"); err != nil || !strings.Contains(out, "session=session-value") || !strings.Contains(out, "/boxface/bin/codex\n1") || !strings.Contains(out, "plain=none") {
		t.Fatalf("login shell after shell init = %q, %v", out, err)
	}
	if out, err := guestSSH(cfg, a.Guest, "tmux new-session -d -s it && tmux show-environment -g | grep -c BOXFACE_IT_SESSION; tmux kill-server"); err != nil || out != "0" {
		t.Fatalf("session value in tmux's server environment: %q, %v", out, err)
	}
	if out, err := guestSSH(cfg, b.Guest, "/usr/local/bin/boxface-run --command codex -- /usr/bin/printenv BOXFACE_IT_TOKEN; echo rc=$?"); err != nil || strings.Contains(out, itValue) {
		t.Fatalf("another environment received the value: %q, %v", out, err)
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

	second := filepath.Join(cfg.StateDir, "key2")
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", second).CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen: %v: %s", err, out)
	}
	secondPublic, err := os.ReadFile(second + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	sshAs := func(key string) error {
		out, err := exec.Command("ssh", "-i", key, "-o", "IdentitiesOnly=yes", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes",
			"-o", "UserKnownHostsFile="+knownHosts, "-o", "ConnectTimeout=10", "dev@"+endpoint.String(), "true").CombinedOutput()
		if err != nil {
			return fmt.Errorf("%w: %s", err, out)
		}
		return nil
	}
	if err := sshAs(second); err == nil {
		t.Fatal("a key registered after start was accepted before a restart")
	}
	restart := func(keys ...string) {
		t.Helper()
		if err := h.StopVM(ctx, ids[0]); err != nil {
			t.Fatal(err)
		}
		spec := specs[ids[0]]
		spec.AuthorizedKeys = keys
		if err := h.StartVM(ctx, spec); err != nil {
			t.Fatal(err)
		}
		if err := h.WaitReady(ctx, ids[0]); err != nil {
			t.Fatal(err)
		}
	}
	restart(strings.TrimSpace(string(publicKey)), strings.TrimSpace(string(secondPublic)))
	if err := sshAs(second); err != nil {
		t.Fatalf("a key passed at start was refused: %v", err)
	}
	restart(strings.TrimSpace(string(secondPublic)))
	if err := sshAs(filepath.Join(cfg.StateDir, "key")); err == nil {
		t.Fatal("a key left out at start was still accepted")
	}
	if err := sshAs(second); err != nil {
		t.Fatalf("the remaining key was refused: %v", err)
	}
	if endpoint, err = h.Endpoint(ctx, ids[0]); err != nil {
		t.Fatal(err)
	}

	for _, id := range ids {
		stopped := time.Now()
		for i := 0; i < 2; i++ {
			if err := h.StopVM(ctx, id); err != nil {
				t.Fatalf("StopVM %s (call %d): %v", id, i+1, err)
			}
		}
		t.Logf("%s: stopped in %s", id, time.Since(stopped).Round(time.Millisecond))
		if _, err := h.Endpoint(ctx, id); err == nil {
			t.Fatalf("Endpoint %s succeeded for a stopped VM", id)
		}
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
	if leftovers, _ := filepath.Glob(filepath.Join(h.disksDir(), "*")); len(leftovers) != 0 {
		t.Fatalf("disk files left: %v", leftovers)
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

func TestIntegrationProfileDrive(t *testing.T) {
	cfg := integrationConfig(t)
	base, profile := os.Getenv("BOXFACE_IT_BASE"), os.Getenv("BOXFACE_IT_PROFILE")
	if base == "" || profile == "" {
		t.Skip("BOXFACE_IT_BASE (a directory with vmlinux and rootfs.ext4 holding no profile) and BOXFACE_IT_PROFILE (a profile drive) are not set")
	}
	ctx := context.Background()
	if err := os.Symlink(base, filepath.Join(cfg.ImagesDir, "it-base")); err != nil {
		t.Fatal(err)
	}
	h, err := New(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	if err := os.Symlink(profile, h.profilePath("it-profile")); err != nil {
		t.Fatal(err)
	}

	suffix := make([]byte, 4)
	rand.Read(suffix)
	id := "it-p-" + hex.EncodeToString(suffix)
	t.Cleanup(func() {
		exec.Command("systemctl", "stop", unitName(id)).Run()
		exec.Command("ip", "link", "del", "dev", tapName(id)).Run()
	})
	publicKey, err := os.ReadFile(filepath.Join(cfg.StateDir, "key.pub"))
	if err != nil {
		t.Fatal(err)
	}
	disk := lifecycle.DiskSpec{DiskID: id + "-disk", EnvironmentID: id, SizeGiB: 4}
	spec := lifecycle.VMSpec{EnvironmentID: id, DiskID: disk.DiskID, ImageID: "it-base", VCPUs: 1, MemoryMiB: 1024,
		AuthorizedKeys: []string{strings.TrimSpace(string(publicKey))}, ProfileID: "it-profile"}
	started := time.Now()
	for name, err := range map[string]error{"CreateDisk": h.CreateDisk(ctx, disk), "CreateNetwork": h.CreateNetwork(ctx, id)} {
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if err := h.StartVM(ctx, spec); err != nil {
		t.Fatal(err)
	}
	if err := h.WaitReady(ctx, id); err != nil {
		t.Fatal(err)
	}
	t.Logf("base image with a profile drive: create to ready in %s", time.Since(started).Round(time.Millisecond))

	a, err := h.readAddressing(id)
	if err != nil {
		t.Fatal(err)
	}
	out, err := guestSSH(cfg, a.Guest, "findmnt -no SOURCE,OPTIONS /nix | cut -d, -f1; readlink -f /opt/boxface/profile | cut -c1-11; bash -lc 'claude --version'; touch /nix/x 2>&1 | grep -c 'Read-only'; for _ in $(seq 1 30); do [ -f ~/.local/state/boxface/personalisation ] && break; sleep 1; done; grep -c '^revision: ' ~/.local/state/boxface/personalisation")
	if err != nil {
		t.Fatalf("guest checks: %v", err)
	}
	if want := "/dev/vdd ro\n/nix/store/\n"; !strings.HasPrefix(out, want) || !strings.Contains(out, "(Claude Code)") || !strings.HasSuffix(out, "1\n1") {
		t.Fatalf("guest checks = %q", out)
	}

	if err := h.StopVM(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := h.RemoveNetwork(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := h.RemoveDisk(ctx, disk.DiskID); err != nil {
		t.Fatal(err)
	}
}

func TestIntegrationBuild(t *testing.T) {
	cfg := integrationConfig(t)
	cfg.BuilderImage = os.Getenv("BOXFACE_IT_BUILDER")
	if cfg.BuilderImage == "" {
		t.Skip("BOXFACE_IT_BUILDER (a directory with the builder's vmlinux and rootfs.ext4) is not set")
	}
	ctx := context.Background()
	h, err := New(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	t.Cleanup(func() {
		exec.Command("ip", "link", "del", "dev", buildTap).Run()
	})
	suffix := make([]byte, 4)
	rand.Read(suffix)
	owner := "it-owner-" + hex.EncodeToString(suffix)
	spec := func(id string, s profiles.Spec) lifecycle.BuildSpec {
		n, err := s.Normalize()
		if err != nil {
			t.Fatal(err)
		}
		return lifecycle.BuildSpec{BuildID: id, OwnerID: owner, Files: profiles.Flake(n), VCPUs: 2, MemoryMiB: 2048, TimeoutSec: 1800}
	}
	wait := func(id string) lifecycle.BuildStatus {
		t.Helper()
		for start := time.Now(); time.Since(start) < 25*time.Minute; time.Sleep(3 * time.Second) {
			st, err := h.BuildStatus(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if st.State != "running" {
				return st
			}
		}
		t.Fatalf("build %s did not finish", id)
		return lifecycle.BuildStatus{}
	}

	good := "it-good-" + hex.EncodeToString(suffix)
	started := time.Now()
	if err := h.StartBuild(ctx, spec(good, profiles.Spec{Packages: []string{"jq"}})); err != nil {
		t.Fatal(err)
	}
	if err := h.StartBuild(ctx, spec("it-other-"+hex.EncodeToString(suffix), profiles.Spec{Packages: []string{"fd"}})); err == nil || !strings.Contains(err.Error(), "another build") {
		t.Fatalf("second concurrent build: err = %v", err)
	}
	st := wait(good)
	t.Logf("cold build of a small profile: %s", time.Since(started).Round(time.Second))
	if st.State != "ready" || !strings.HasPrefix(st.HomePath, "/nix/store/") || st.ClosureMiB == 0 {
		t.Fatalf("good build = %+v", st)
	}
	if label, err := run(ctx, "blkid", "-o", "value", "-s", "LABEL", h.profilePath(good)); err != nil || strings.TrimSpace(string(label)) != "boxface-profile" {
		t.Fatalf("published drive label = %q, %v", label, err)
	}
	if err := h.StartBuild(ctx, spec(good, profiles.Spec{Packages: []string{"jq"}})); err != nil {
		t.Fatalf("restarting a finished build: %v", err)
	}
	if again, _ := h.BuildStatus(ctx, good); again.State != "ready" {
		t.Fatalf("status after restart = %+v", again)
	}

	broken := "it-broken-" + hex.EncodeToString(suffix)
	if err := h.StartBuild(ctx, spec(broken, profiles.Spec{Module: "{ pkgs, ... }: { home.packages = [ pkgs.jq "})); err != nil {
		t.Fatal(err)
	}
	if st := wait(broken); st.State != "failed" || st.Error != "nix build failed" || !strings.Contains(st.LogTail, "syntax error") {
		t.Fatalf("broken module = %+v", st)
	}
	if _, err := os.Stat(h.profilePath(broken)); err == nil {
		t.Fatal("a failed build published a drive")
	}

	a, _ := h.buildAddressing()
	probe := "probe-" + hex.EncodeToString(suffix)
	module := fmt.Sprintf("{ pkgs, ... }: { home.packages = [ (pkgs.runCommand \"probe\" { } \"${pkgs.curl}/bin/curl --connect-timeout 10 -sS http://%s:22/ -o $out\") ]; }", a.Host)
	if err := h.StartBuild(ctx, spec(probe, profiles.Spec{Module: module})); err != nil {
		t.Fatal(err)
	}
	if st := wait(probe); st.State != "failed" || !strings.Contains(st.LogTail, "Connection timed out") {
		var lines []string
		for _, l := range strings.Split(st.LogTail, "\n") {
			if strings.Contains(l, "curl") || strings.Contains(l, "probe") {
				lines = append(lines, l)
			}
		}
		t.Fatalf("the build's connection to host SSH was not dropped: %s: %s", st.Error, strings.Join(lines, "\n"))
	}
}
