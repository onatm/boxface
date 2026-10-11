package worker

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
)

const (
	minFreeForBuild = 10 << 30
	minFreeForDisk  = 2 << 30
)

type Storage struct {
	TotalBytes         int64 `json:"total_bytes"`
	FreeBytes          int64 `json:"free_bytes"`
	ImagesBytes        int64 `json:"images_bytes"`
	ProfileDrivesBytes int64 `json:"profile_drives_bytes"`
	BuildCachesBytes   int64 `json:"build_caches_bytes"`
	DataDisksBytes     int64 `json:"data_disks_bytes"`
}

func allocated(root string) int64 {
	var total int64
	filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if st, ok := info.Sys().(*syscall.Stat_t); ok {
			total += int64(st.Blocks) * 512
		} else {
			total += info.Size()
		}
		return nil
	})
	return total
}

func (h *Host) free() (total, free int64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(h.cfg.StateDir, &st); err != nil {
		return 0, 0, err
	}
	return int64(st.Blocks) * int64(st.Bsize), int64(st.Bavail) * int64(st.Bsize), nil
}

func (h *Host) requireFree(bytes int64, what string) error {
	_, free, err := h.free()
	if err != nil {
		return err
	}
	if free < bytes {
		return fmt.Errorf("not enough free disk space to %s: %d GiB free, %d GiB needed; delete unused images, profiles, or environments", what, free>>30, bytes>>30)
	}
	return nil
}

func (h *Host) Storage(ctx context.Context) (Storage, error) {
	total, free, err := h.free()
	if err != nil {
		return Storage{}, err
	}
	return Storage{
		TotalBytes:         total,
		FreeBytes:          free,
		ImagesBytes:        allocated(h.cfg.ImagesDir),
		ProfileDrivesBytes: allocated(h.profilesDir()),
		BuildCachesBytes:   allocated(filepath.Join(h.buildsDir(), "cache")),
		DataDisksBytes:     allocated(h.disksDir()),
	}, nil
}

func (h *Host) ProfileDrives(ctx context.Context) ([]string, error) {
	entries, err := os.ReadDir(h.profilesDir())
	if err != nil {
		return nil, err
	}
	keys := []string{}
	for _, e := range entries {
		if key, ok := strings.CutSuffix(e.Name(), ".img"); ok && validID(key) == nil {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	return keys, nil
}

func (h *Host) RemoveProfileDrive(ctx context.Context, key string) error {
	if err := validID(key); err != nil {
		return err
	}
	path := h.profilePath(key)
	entries, err := os.ReadDir(h.environmentsDir())
	if err != nil {
		return err
	}
	for _, e := range entries {
		config, err := os.ReadFile(filepath.Join(h.envDir(e.Name()), "firecracker.json"))
		if err != nil || !strings.Contains(string(config), `"`+path+`"`) {
			continue
		}
		if active, err := unitActive(ctx, unitName(e.Name())); err != nil {
			return err
		} else if active {
			return fmt.Errorf("profile drive %s is attached to running environment %s", key, e.Name())
		}
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.RemoveAll(h.buildDir(key))
}

func (h *Host) RemoveBuildCache(ctx context.Context, ownerID string) error {
	if err := validID(ownerID); err != nil {
		return err
	}
	h.buildMu.Lock()
	defer h.buildMu.Unlock()
	if running, err := h.runningBuild(ctx); err != nil {
		return err
	} else if running != "" {
		return fmt.Errorf("a build (%s) is running; clear the cache after it finishes", running)
	}
	if err := os.Remove(h.cachePath(ownerID)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
