package worker

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/onatm/boxface/internal/lifecycle"
)

const (
	buildTap        = "bfxbuild"
	cacheSizeGiB    = 40
	outputSizeGiB   = 16
	resultSize      = 64 << 10
	buildLogLines   = 200
	buildMemoryMax  = 64 << 10
	buildTimeoutMax = 6 * 3600
)

var buildFiles = []string{"flake.nix", "flake.lock", "user.nix"}

func buildUnit(id string) string { return "boxface-build-" + id + ".service" }

func (h *Host) buildsDir() string         { return filepath.Join(h.cfg.StateDir, "builds") }
func (h *Host) buildDir(id string) string { return filepath.Join(h.buildsDir(), id) }
func (h *Host) cachePath(owner string) string {
	return filepath.Join(h.buildsDir(), "cache", owner+".img")
}

func validateBuild(spec lifecycle.BuildSpec) error {
	for _, id := range []string{spec.BuildID, spec.OwnerID} {
		if err := validID(id); err != nil {
			return err
		}
	}
	if spec.VCPUs < 1 || spec.MemoryMiB < 512 || spec.MemoryMiB > buildMemoryMax || spec.TimeoutSec < 60 || spec.TimeoutSec > buildTimeoutMax {
		return fmt.Errorf("build limits out of range: %d vCPUs, %d MiB, %d s", spec.VCPUs, spec.MemoryMiB, spec.TimeoutSec)
	}
	if spec.Files["flake.nix"] == "" || spec.Files["flake.lock"] == "" {
		return errors.New("build needs flake.nix and flake.lock")
	}
	for name := range spec.Files {
		if !slices.Contains(buildFiles, name) {
			return fmt.Errorf("unexpected build input %q", name)
		}
	}
	for _, v := range []string{spec.DotfilesRepo, spec.DotfilesRef} {
		if strings.ContainsAny(v, "\x00\r\n") {
			return errors.New("dotfiles repository and ref must be single lines")
		}
	}
	return nil
}

func (h *Host) StartBuild(ctx context.Context, spec lifecycle.BuildSpec) error {
	if err := validateBuild(spec); err != nil {
		return err
	}
	if ok, err := exists(h.profilePath(spec.BuildID)); err != nil || ok {
		return err
	}
	h.buildMu.Lock()
	defer h.buildMu.Unlock()
	if active, err := unitActive(ctx, buildUnit(spec.BuildID)); err != nil || active {
		return err
	}
	if running, err := h.runningBuild(ctx); err != nil {
		return err
	} else if running != "" {
		return fmt.Errorf("another build (%s) is running", running)
	}
	if err := h.requireFree(minFreeForBuild, "build a profile"); err != nil {
		return err
	}
	for _, p := range []string{filepath.Join(h.cfg.BuilderImage, "vmlinux"), filepath.Join(h.cfg.BuilderImage, "rootfs.ext4")} {
		if _, err := os.Stat(p); err != nil {
			return fmt.Errorf("builder image: %w", err)
		}
	}

	dir := h.buildDir(spec.BuildID)
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	inputs := filepath.Join(dir, "inputs")
	if err := os.MkdirAll(filepath.Join(inputs, "flake"), 0o755); err != nil {
		return err
	}
	for name, content := range spec.Files {
		if err := os.WriteFile(filepath.Join(inputs, "flake", name), []byte(content), 0o644); err != nil {
			return err
		}
	}
	if spec.DotfilesRepo != "" {
		os.WriteFile(filepath.Join(inputs, "dotfiles-repo"), []byte(spec.DotfilesRepo+"\n"), 0o644)
		os.WriteFile(filepath.Join(inputs, "dotfiles-ref"), []byte(spec.DotfilesRef+"\n"), 0o644)
	}
	inputsImage := filepath.Join(dir, "inputs.ext4")
	if err := sparseFile(inputsImage, 64<<20); err != nil {
		return err
	}
	if _, err := run(ctx, "mkfs.ext4", "-q", "-L", "boxface-inputs", "-d", inputs, inputsImage); err != nil {
		return err
	}
	os.RemoveAll(inputs)

	cache := h.cachePath(spec.OwnerID)
	if err := os.MkdirAll(filepath.Dir(cache), 0o755); err != nil {
		return err
	}
	if ok, err := exists(cache); err != nil {
		return err
	} else if !ok {
		if err := sparseFile(cache, cacheSizeGiB<<30); err != nil {
			return err
		}
	}
	output := h.profilePath(spec.BuildID) + ".tmp"
	if err := sparseFile(output, outputSizeGiB<<30); err != nil {
		return err
	}
	result := filepath.Join(dir, "result.img")
	if err := sparseFile(result, resultSize); err != nil {
		return err
	}
	for _, p := range []string{dir, inputsImage, cache, output, result} {
		if err := os.Chown(p, h.vmUID, h.vmGID); err != nil {
			return err
		}
	}

	a, err := h.buildAddressing()
	if err != nil {
		return err
	}
	if !linkExists(ctx, buildTap) {
		if _, err := run(ctx, "ip", "tuntap", "add", "dev", buildTap, "mode", "tap", "user", strconv.Itoa(h.vmUID)); err != nil {
			return err
		}
	}
	if _, err := run(ctx, "ip", "addr", "replace", a.Host.String()+"/30", "dev", buildTap); err != nil {
		return err
	}
	if _, err := run(ctx, "ip", "link", "set", "dev", buildTap, "up"); err != nil {
		return err
	}

	type drive struct {
		DriveID      string `json:"drive_id"`
		PathOnHost   string `json:"path_on_host"`
		IsRootDevice bool   `json:"is_root_device"`
		IsReadOnly   bool   `json:"is_read_only"`
	}
	guest := a.Guest.As4()
	config, err := json.MarshalIndent(map[string]any{
		"boot-source": map[string]any{
			"kernel_image_path": filepath.Join(h.cfg.BuilderImage, "vmlinux"),
			"boot_args": fmt.Sprintf("console=ttyS0 reboot=k panic=1 pci=off net.ifnames=0 ip=%s::%s:255.255.255.252::eth0:off",
				a.Guest, a.Host),
		},
		"drives": []drive{
			{"rootfs", filepath.Join(h.cfg.BuilderImage, "rootfs.ext4"), true, true},
			{"cache", cache, false, false},
			{"inputs", inputsImage, false, true},
			{"output", output, false, false},
			{"result", result, false, false},
		},
		"machine-config": map[string]any{"vcpu_count": spec.VCPUs, "mem_size_mib": spec.MemoryMiB},
		"network-interfaces": []map[string]any{{
			"iface_id":      "eth0",
			"guest_mac":     fmt.Sprintf("06:00:%02x:%02x:%02x:%02x", guest[0], guest[1], guest[2], guest[3]),
			"host_dev_name": buildTap,
		}},
	}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "firecracker.json"), config, 0o644); err != nil {
		return err
	}
	unit := buildUnit(spec.BuildID)
	run(ctx, "systemctl", "reset-failed", unit)
	_, err = run(ctx, "systemd-run",
		"--unit", unit,
		"--property", "User="+h.cfg.VMUser,
		"--property", "Group="+h.cfg.VMUser,
		"--property", "SupplementaryGroups=kvm",
		"--property", "MemoryMax="+strconv.Itoa(spec.MemoryMiB+memoryOverheadMi)+"M",
		"--property", "RuntimeMaxSec="+strconv.Itoa(spec.TimeoutSec),
		"--property", "TimeoutStopSec=10",
		"--",
		h.cfg.Firecracker, "--api-sock", filepath.Join(dir, "api.sock"), "--config-file", filepath.Join(dir, "firecracker.json"),
	)
	return err
}

func sparseFile(path string, size int64) error {
	os.Remove(path)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err := f.Truncate(size); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func (h *Host) runningBuild(ctx context.Context) (string, error) {
	out, err := run(ctx, "systemctl", "list-units", "--plain", "--no-legend", "--state=active,activating", "boxface-build-*")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(out), "\n") {
		if fields := strings.Fields(line); len(fields) > 0 {
			return strings.TrimSuffix(strings.TrimPrefix(fields[0], "boxface-build-"), ".service"), nil
		}
	}
	return "", nil
}

type builderResult struct {
	Status         string `json:"status"`
	HomePath       string `json:"home_path"`
	ClosureMiB     int    `json:"closure_mib"`
	DotfilesCommit string `json:"dotfiles_commit"`
	Error          string `json:"error"`
}

func (h *Host) BuildStatus(ctx context.Context, buildID string) (lifecycle.BuildStatus, error) {
	if err := validID(buildID); err != nil {
		return lifecycle.BuildStatus{}, err
	}
	h.buildMu.Lock()
	defer h.buildMu.Unlock()
	dir := h.buildDir(buildID)
	saved := filepath.Join(dir, "status.json")
	if b, err := os.ReadFile(saved); err == nil {
		var st lifecycle.BuildStatus
		if err := json.Unmarshal(b, &st); err != nil {
			return lifecycle.BuildStatus{}, err
		}
		return st, nil
	}
	if ok, err := exists(h.profilePath(buildID)); err != nil {
		return lifecycle.BuildStatus{}, err
	} else if ok {
		return lifecycle.BuildStatus{State: "ready"}, nil
	}
	if ok, err := exists(dir); err != nil {
		return lifecycle.BuildStatus{}, err
	} else if !ok {
		return lifecycle.BuildStatus{State: "unknown"}, nil
	}
	unit := buildUnit(buildID)
	if active, err := unitActive(ctx, unit); err != nil {
		return lifecycle.BuildStatus{}, err
	} else if active {
		return lifecycle.BuildStatus{State: "running"}, nil
	}

	st := h.collect(ctx, buildID)
	b, err := json.Marshal(st)
	if err != nil {
		return lifecycle.BuildStatus{}, err
	}
	if err := os.WriteFile(saved, b, 0o644); err != nil {
		return lifecycle.BuildStatus{}, err
	}
	for _, name := range []string{"inputs.ext4", "result.img", "api.sock"} {
		os.Remove(filepath.Join(dir, name))
	}
	os.Remove(h.profilePath(buildID) + ".tmp")
	run(ctx, "systemctl", "reset-failed", unit)
	if linkExists(ctx, buildTap) {
		run(ctx, "ip", "link", "del", "dev", buildTap)
	}
	return st, nil
}

func (h *Host) collect(ctx context.Context, buildID string) lifecycle.BuildStatus {
	dir := h.buildDir(buildID)
	raw, _ := os.ReadFile(filepath.Join(dir, "result.img"))
	raw = bytes.TrimRight(raw, "\x00")
	first, rest, _ := bytes.Cut(raw, []byte("\n"))
	var r builderResult
	if len(first) == 0 || json.Unmarshal(first, &r) != nil {
		journal, _ := run(context.Background(), "journalctl", "--unit", buildUnit(buildID), "--lines", strconv.Itoa(buildLogLines), "--no-pager", "--output", "cat")
		return lifecycle.BuildStatus{State: "failed", Error: "the builder exited without a result (out of memory or over the time limit?): " + consoleSummary(string(journal)), LogTail: tail(string(journal))}
	}
	st := lifecycle.BuildStatus{LogTail: tail(string(rest)), DotfilesCommit: r.DotfilesCommit}
	if r.Status != "ok" {
		st.State, st.Error = "failed", cmp.Or(r.Error, "the build failed")
		return st
	}
	output := h.profilePath(buildID) + ".tmp"
	label, err := run(ctx, "blkid", "-o", "value", "-s", "LABEL", output)
	if err != nil || strings.TrimSpace(string(label)) != "boxface-profile" {
		st.State, st.Error = "failed", "the builder reported success but the profile drive is not valid"
		return st
	}
	err = os.Chown(output, 0, 0)
	if err == nil {
		err = os.Chmod(output, 0o644)
	}
	if err == nil {
		err = os.Rename(output, h.profilePath(buildID))
	}
	if err != nil {
		st.State, st.Error = "failed", "could not publish the profile drive"
		return st
	}
	st.State, st.HomePath, st.ClosureMiB = "ready", r.HomePath, r.ClosureMiB
	return st
}

func tail(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > buildLogLines {
		lines = lines[len(lines)-buildLogLines:]
	}
	return strings.Join(lines, "\n")
}
