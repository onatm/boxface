package worker

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const tapPrefix = "bfx"

type addressing struct {
	Index int
	Host  netip.Addr
	Guest netip.Addr
}

func tapName(environmentID string) string {
	name := tapPrefix + strings.ReplaceAll(environmentID, "-", "")
	if len(name) > 15 {
		name = name[:15]
	}
	return name
}

func (h *Host) addressing(index int) (addressing, error) {
	slots := 1 << (32 - h.cfg.Pool.Bits() - 2)
	if index < 0 || index >= slots {
		return addressing{}, fmt.Errorf("address index %d outside pool %s", index, h.cfg.Pool)
	}
	base := h.cfg.Pool.Masked().Addr().As4()
	n := uint32(base[0])<<24 | uint32(base[1])<<16 | uint32(base[2])<<8 | uint32(base[3])
	n += uint32(index) * 4
	addr := func(v uint32) netip.Addr {
		return netip.AddrFrom4([4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)})
	}
	return addressing{Index: index, Host: addr(n + 1), Guest: addr(n + 2)}, nil
}

func (h *Host) readAddressing(environmentID string) (addressing, error) {
	b, err := os.ReadFile(filepath.Join(h.envDir(environmentID), "network"))
	if err != nil {
		return addressing{}, err
	}
	index, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return addressing{}, fmt.Errorf("environment %s network allocation: %w", environmentID, err)
	}
	return h.addressing(index)
}

func (h *Host) allocate(environmentID string) (addressing, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if a, err := h.readAddressing(environmentID); err == nil {
		return a, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return addressing{}, err
	}

	used := map[int]bool{}
	entries, err := os.ReadDir(h.environmentsDir())
	if err != nil {
		return addressing{}, err
	}
	for _, e := range entries {
		if a, err := h.readAddressing(e.Name()); err == nil {
			used[a.Index] = true
		}
	}
	index := 0
	for used[index] {
		index++
	}
	a, err := h.addressing(index)
	if err != nil {
		return addressing{}, fmt.Errorf("network pool exhausted: %w", err)
	}

	dir := h.envDir(environmentID)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return addressing{}, err
	}
	if err := os.Chown(dir, h.vmUID, h.vmGID); err != nil {
		return addressing{}, err
	}
	tmp := filepath.Join(dir, "network.tmp")
	if err := os.WriteFile(tmp, []byte(strconv.Itoa(index)+"\n"), 0o644); err != nil {
		return addressing{}, err
	}
	return a, os.Rename(tmp, filepath.Join(dir, "network"))
}

func linkExists(ctx context.Context, name string) bool {
	_, err := run(ctx, "ip", "link", "show", "dev", name)
	return err == nil
}

func (h *Host) CreateNetwork(ctx context.Context, environmentID string) error {
	if err := validID(environmentID); err != nil {
		return err
	}
	a, err := h.allocate(environmentID)
	if err != nil {
		return err
	}
	tap := tapName(environmentID)
	if !linkExists(ctx, tap) {
		if _, err := run(ctx, "ip", "tuntap", "add", "dev", tap, "mode", "tap", "user", strconv.Itoa(h.vmUID)); err != nil {
			return err
		}
	}
	if _, err := run(ctx, "ip", "addr", "replace", a.Host.String()+"/30", "dev", tap); err != nil {
		return err
	}
	_, err = run(ctx, "ip", "link", "set", "dev", tap, "up")
	return err
}

func (h *Host) RemoveNetwork(ctx context.Context, environmentID string) error {
	if err := validID(environmentID); err != nil {
		return err
	}
	if active, err := unitActive(ctx, unitName(environmentID)); err != nil {
		return err
	} else if active {
		return fmt.Errorf("environment %s VM is still running", environmentID)
	}
	tap := tapName(environmentID)
	if linkExists(ctx, tap) {
		if _, err := run(ctx, "ip", "link", "del", "dev", tap); err != nil {
			return err
		}
	}
	h.stopListening(environmentID)
	return os.RemoveAll(h.envDir(environmentID))
}

func (h *Host) firewallRuleset() string {
	return fmt.Sprintf(`table inet boxface
delete table inet boxface
table inet boxface {
	chain input {
		type filter hook input priority filter; policy accept;
		iifname "%[1]s*" ct state established,related accept
		iifname "%[1]s*" drop
	}
	chain forward {
		type filter hook forward priority filter; policy accept;
		iifname "%[1]s*" oifname "%[1]s*" drop
		iifname "%[1]s*" accept
		oifname "%[1]s*" ct state established,related accept
		oifname "%[1]s*" drop
	}
	chain postrouting {
		type nat hook postrouting priority srcnat; policy accept;
		ip saddr %[2]s oifname != "%[1]s*" masquerade
	}
}
`, tapPrefix, h.cfg.Pool.Masked())
}

func (h *Host) applyFirewall(ctx context.Context) error {
	cmd := commandWithStdin(ctx, h.firewallRuleset(), "nft", "-f", "-")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("apply nftables ruleset: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
