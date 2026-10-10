package worker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const guestUID = 1000

type Config struct {
	StateDir     string
	ImagesDir    string
	Firecracker  string
	VMUser       string
	Pool         netip.Prefix
	ReadyTimeout time.Duration
	StopTimeout  time.Duration
}

type Host struct {
	cfg          Config
	vmUID, vmGID int

	mu        sync.Mutex
	listeners map[string]net.Listener
}

func New(ctx context.Context, cfg Config) (*Host, error) {
	if !cfg.Pool.IsValid() || !cfg.Pool.Addr().Is4() || cfg.Pool.Bits() > 30 {
		return nil, fmt.Errorf("pool %s must be an IPv4 prefix of /30 or larger", cfg.Pool)
	}
	u, err := user.Lookup(cfg.VMUser)
	if err != nil {
		return nil, fmt.Errorf("vm user: %w", err)
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	h := &Host{cfg: cfg, vmUID: uid, vmGID: gid, listeners: map[string]net.Listener{}}

	for _, dir := range []string{h.environmentsDir(), h.disksDir()} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	if err := os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1"), 0o644); err != nil {
		return nil, fmt.Errorf("enable ip forwarding: %w", err)
	}
	if err := h.applyFirewall(ctx); err != nil {
		return nil, err
	}
	if err := h.recoverListeners(ctx); err != nil {
		return nil, err
	}
	return h, nil
}

func (h *Host) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for id, l := range h.listeners {
		l.Close()
		delete(h.listeners, id)
	}
}

func (h *Host) environmentsDir() string { return filepath.Join(h.cfg.StateDir, "environments") }
func (h *Host) disksDir() string        { return filepath.Join(h.cfg.StateDir, "disks") }
func (h *Host) envDir(id string) string { return filepath.Join(h.environmentsDir(), id) }
func (h *Host) diskPath(id string) string {
	return filepath.Join(h.disksDir(), id+".ext4")
}

func validID(id string) error {
	if id == "" || len(id) > 64 || strings.Trim(id, "0123456789abcdefghijklmnopqrstuvwxyz-") != "" {
		return fmt.Errorf("invalid identifier %q", id)
	}
	return nil
}

func run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return out.Bytes(), fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(out.String()))
	}
	return out.Bytes(), nil
}

func exists(path string) (bool, error) {
	_, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}
