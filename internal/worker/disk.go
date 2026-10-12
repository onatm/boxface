package worker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

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

	keys, err := os.ReadFile(h.cfg.AuthorizedKeysFile)
	if err != nil {
		return fmt.Errorf("read authorized keys: %w", err)
	}
	seed, err := os.MkdirTemp("", "boxface-seed-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(seed)
	sshDir := filepath.Join(seed, "home", ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(sshDir, "authorized_keys"), keys, 0o600); err != nil {
		return err
	}
	for _, p := range []string{filepath.Join(seed, "home"), sshDir, filepath.Join(sshDir, "authorized_keys")} {
		if err := os.Chown(p, guestUID, guestUID); err != nil {
			return err
		}
	}
	if err := os.Chmod(filepath.Join(seed, "home"), 0o700); err != nil {
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
	return os.Rename(tmp, path)
}

func (h *Host) RemoveDisk(ctx context.Context, diskID string) error {
	if err := validID(diskID); err != nil {
		return err
	}
	path := h.diskPath(diskID)
	for _, p := range []string{path + ".tmp", path} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}
