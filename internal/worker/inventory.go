package worker

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type EnvironmentResources struct {
	EnvironmentID string `json:"environment_id"`
	Network       bool   `json:"network"`
	VM            bool   `json:"vm"`
	Ready         bool   `json:"ready"`
}

type Inventory struct {
	Environments []EnvironmentResources `json:"environments"`
	Disks        []string               `json:"disks"`
}

func (h *Host) Inventory(ctx context.Context) (Inventory, error) {
	ids := map[string]bool{}
	entries, err := os.ReadDir(h.environmentsDir())
	if err != nil {
		return Inventory{}, err
	}
	for _, e := range entries {
		if validID(e.Name()) == nil {
			ids[e.Name()] = true
		}
	}
	out, err := run(ctx, "systemctl", "list-units", "--all", "--plain", "--no-legend", "boxface-vm-*")
	if err != nil {
		return Inventory{}, err
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		id := strings.TrimSuffix(strings.TrimPrefix(fields[0], "boxface-vm-"), ".service")
		if validID(id) == nil {
			ids[id] = true
		}
	}

	inv := Inventory{Environments: []EnvironmentResources{}, Disks: []string{}}
	for id := range ids {
		vm, err := unitActive(ctx, unitName(id))
		if err != nil {
			return Inventory{}, err
		}
		ready, err := exists(filepath.Join(h.envDir(id), "ready"))
		if err != nil {
			return Inventory{}, err
		}
		inv.Environments = append(inv.Environments, EnvironmentResources{
			EnvironmentID: id,
			Network:       linkExists(ctx, tapName(id)),
			VM:            vm,
			Ready:         vm && ready,
		})
	}
	sort.Slice(inv.Environments, func(i, j int) bool { return inv.Environments[i].EnvironmentID < inv.Environments[j].EnvironmentID })

	disks, err := os.ReadDir(h.disksDir())
	if err != nil {
		return Inventory{}, err
	}
	for _, d := range disks {
		if id, ok := strings.CutSuffix(d.Name(), ".ext4"); ok {
			inv.Disks = append(inv.Disks, id)
		}
	}
	return inv, nil
}
