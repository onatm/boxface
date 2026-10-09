package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/onatm/boxface/internal/lifecycle"
)

const (
	readyPort        = 1024
	memoryOverheadMi = 256
)

func unitName(environmentID string) string { return "boxface-vm-" + environmentID + ".service" }

func unitActive(ctx context.Context, unit string) (bool, error) {
	out, err := exec.CommandContext(ctx, "systemctl", "is-active", unit).Output()
	state := strings.TrimSpace(string(out))
	if state == "active" || state == "activating" || state == "deactivating" {
		return true, nil
	}
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		return false, fmt.Errorf("systemctl is-active %s: %w", unit, err)
	}
	return false, nil
}

func commandWithStdin(ctx context.Context, stdin string, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = strings.NewReader(stdin)
	return cmd
}

func (h *Host) firecrackerConfig(spec lifecycle.VMSpec, a addressing) ([]byte, error) {
	dir := h.envDir(spec.EnvironmentID)
	guest := a.Guest.As4()
	type drive struct {
		DriveID      string `json:"drive_id"`
		PathOnHost   string `json:"path_on_host"`
		IsRootDevice bool   `json:"is_root_device"`
		IsReadOnly   bool   `json:"is_read_only"`
	}
	config := map[string]any{
		"boot-source": map[string]any{
			"kernel_image_path": filepath.Join(h.cfg.ImagesDir, spec.ImageID, "vmlinux"),
			"boot_args": fmt.Sprintf("console=ttyS0 reboot=k panic=1 pci=off net.ifnames=0 ip=%s::%s:255.255.255.252::eth0:off",
				a.Guest, a.Host),
		},
		"drives": []drive{
			{"rootfs", filepath.Join(h.cfg.ImagesDir, spec.ImageID, "rootfs.ext4"), true, true},
			{"data", h.diskPath(spec.DiskID), false, false},
		},
		"machine-config": map[string]any{"vcpu_count": spec.VCPUs, "mem_size_mib": spec.MemoryMiB},
		"network-interfaces": []map[string]any{{
			"iface_id":      "eth0",
			"guest_mac":     fmt.Sprintf("06:00:%02x:%02x:%02x:%02x", guest[0], guest[1], guest[2], guest[3]),
			"host_dev_name": tapName(spec.EnvironmentID),
		}},
		"vsock": map[string]any{"guest_cid": 3, "uds_path": filepath.Join(dir, "vsock.sock")},
	}
	return json.MarshalIndent(config, "", "  ")
}

func (h *Host) StartVM(ctx context.Context, spec lifecycle.VMSpec) error {
	for _, id := range []string{spec.EnvironmentID, spec.DiskID, spec.ImageID} {
		if err := validID(id); err != nil {
			return err
		}
	}
	unit := unitName(spec.EnvironmentID)
	if active, err := unitActive(ctx, unit); err != nil {
		return err
	} else if active {
		return h.listen(spec.EnvironmentID)
	}

	a, err := h.readAddressing(spec.EnvironmentID)
	if err != nil {
		return fmt.Errorf("environment %s network: %w", spec.EnvironmentID, err)
	}
	for _, p := range []string{
		filepath.Join(h.cfg.ImagesDir, spec.ImageID, "vmlinux"),
		filepath.Join(h.cfg.ImagesDir, spec.ImageID, "rootfs.ext4"),
		h.diskPath(spec.DiskID),
	} {
		if _, err := os.Stat(p); err != nil {
			return err
		}
	}

	dir := h.envDir(spec.EnvironmentID)
	config, err := h.firecrackerConfig(spec, a)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "firecracker.json"), config, 0o644); err != nil {
		return err
	}
	for _, name := range []string{"ready", "api.sock", "vsock.sock"} {
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if err := h.listen(spec.EnvironmentID); err != nil {
		return err
	}

	run(ctx, "systemctl", "reset-failed", unit)
	_, err = run(ctx, "systemd-run",
		"--unit", unit,
		"--collect",
		"--property", "User="+h.cfg.VMUser,
		"--property", "Group="+h.cfg.VMUser,
		"--property", "SupplementaryGroups=kvm",
		"--property", "MemoryMax="+strconv.Itoa(spec.MemoryMiB+memoryOverheadMi)+"M",
		"--property", "TimeoutStopSec=10",
		"--",
		h.cfg.Firecracker, "--api-sock", filepath.Join(dir, "api.sock"), "--config-file", filepath.Join(dir, "firecracker.json"),
	)
	return err
}

func (h *Host) WaitReady(ctx context.Context, environmentID string) error {
	if err := validID(environmentID); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, h.cfg.ReadyTimeout)
	defer cancel()
	unit := unitName(environmentID)
	ready := filepath.Join(h.envDir(environmentID), "ready")
	for {
		if ok, err := exists(ready); err != nil || ok {
			return err
		}
		active, err := unitActive(ctx, unit)
		if err != nil {
			return err
		}
		if !active {
			out, _ := run(context.Background(), "journalctl", "--unit", unit, "--lines", "20", "--no-pager", "--output", "cat")
			return fmt.Errorf("VM exited before reporting readiness: %s", strings.TrimSpace(string(out)))
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("guest did not report readiness within %s: %w", h.cfg.ReadyTimeout, ctx.Err())
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func (h *Host) StopVM(ctx context.Context, environmentID string) error {
	if err := validID(environmentID); err != nil {
		return err
	}
	unit := unitName(environmentID)
	dir := h.envDir(environmentID)
	active, err := unitActive(ctx, unit)
	if err != nil {
		return err
	}
	if active {
		sendCtrlAltDel(ctx, filepath.Join(dir, "api.sock"))
		deadline := time.Now().Add(h.cfg.StopTimeout)
		for active && time.Now().Before(deadline) {
			time.Sleep(250 * time.Millisecond)
			if active, err = unitActive(ctx, unit); err != nil {
				return err
			}
		}
		if active {
			if _, err := run(ctx, "systemctl", "stop", unit); err != nil {
				return err
			}
		}
	}
	h.stopListening(environmentID)
	if err := os.Remove(filepath.Join(dir, "ready")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func sendCtrlAltDel(ctx context.Context, apiSocket string) error {
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", apiSocket)
		},
	}, Timeout: 5 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, "http://firecracker/actions", strings.NewReader(`{"action_type": "SendCtrlAltDel"}`))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func (h *Host) listen(environmentID string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.listeners[environmentID]; ok {
		return nil
	}
	dir := h.envDir(environmentID)
	path := filepath.Join(dir, "vsock.sock_"+strconv.Itoa(readyPort))
	os.Remove(path)
	l, err := net.Listen("unix", path)
	if err != nil {
		return fmt.Errorf("readiness listener: %w", err)
	}
	if err := os.Chown(path, h.vmUID, h.vmGID); err != nil {
		l.Close()
		return err
	}
	h.listeners[environmentID] = l
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			msg, _ := io.ReadAll(io.LimitReader(conn, 64))
			conn.Close()
			if strings.Contains(string(msg), "ready") {
				os.WriteFile(filepath.Join(dir, "ready"), nil, 0o644)
			}
		}
	}()
	return nil
}

func (h *Host) stopListening(environmentID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if l, ok := h.listeners[environmentID]; ok {
		l.Close()
		delete(h.listeners, environmentID)
	}
}

func (h *Host) recoverListeners(ctx context.Context) error {
	entries, err := os.ReadDir(h.environmentsDir())
	if err != nil {
		return err
	}
	for _, e := range entries {
		id := e.Name()
		if validID(id) != nil {
			continue
		}
		active, err := unitActive(ctx, unitName(id))
		if err != nil {
			return err
		}
		if active {
			if err := h.listen(id); err != nil {
				return err
			}
		}
	}
	return nil
}
