package worker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/onatm/boxface/internal/lifecycle"
)

func (h *Host) CreateDisk(ctx context.Context, spec lifecycle.DiskSpec) error {
	if err := validID(spec.DiskID); err != nil {
		return err
	}
	if spec.SizeGiB < 1 {
		return fmt.Errorf("disk size %d GiB is too small", spec.SizeGiB)
	}
	path := h.diskPath(spec.DiskID)
	if ok, err := exists(path); err != nil || ok {
		return err
	}

	seed, err := os.MkdirTemp("", "boxface-seed-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(seed)
	hostKeyDir := filepath.Join(seed, "boxface", "ssh")
	if err := os.MkdirAll(hostKeyDir, 0o700); err != nil {
		return err
	}
	hostKey := filepath.Join(hostKeyDir, "ssh_host_ed25519_key")
	if _, err := run(ctx, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "", "-f", hostKey); err != nil {
		return err
	}
	if err := os.Chmod(filepath.Join(seed, "boxface"), 0o755); err != nil {
		return err
	}
	hostKeyPub, err := os.ReadFile(hostKey + ".pub")
	if err != nil {
		return err
	}
	if err := os.Remove(hostKey + ".pub"); err != nil {
		return err
	}
	pubPath := h.hostKeyPath(spec.DiskID)
	if err := os.WriteFile(pubPath+".tmp", hostKeyPub, 0o644); err != nil {
		return err
	}

	tmp := path + ".tmp"
	os.Remove(tmp)
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	f.Close()
	if err := os.Truncate(tmp, int64(spec.SizeGiB)<<30); err != nil {
		os.Remove(tmp)
		return err
	}
	if _, err := run(ctx, "mkfs.ext4", "-q", "-L", "boxface-data", "-d", seed, tmp); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Chown(tmp, h.vmUID, h.vmGID); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(pubPath+".tmp", pubPath); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

func (h *Host) HostKey(ctx context.Context, diskID string) (string, error) {
	if err := validID(diskID); err != nil {
		return "", err
	}
	b, err := os.ReadFile(h.hostKeyPath(diskID))
	if errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("disk %s has no recorded host key; recreate the environment", diskID)
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

func (h *Host) RemoveDisk(ctx context.Context, diskID string) error {
	if err := validID(diskID); err != nil {
		return err
	}
	path := h.diskPath(diskID)
	for _, p := range []string{path + ".tmp", path, h.hostKeyPath(diskID) + ".tmp", h.hostKeyPath(diskID)} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}
