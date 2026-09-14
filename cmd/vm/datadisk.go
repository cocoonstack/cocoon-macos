package vm

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/cocoonstack/cocoon/types"
	"github.com/cocoonstack/cocoon/utils"
)

// maxDataDisks: macOS has no virtio-blk, so disks ride ich9-ahci's 6 SATA ports; OpenCoreBoot=sata.2 and MacHDD=sata.4 leave exactly four free.
const maxDataDisks = 4

var agentOnlyDiskKeys = []string{"fstype", "mount", "directio"}

// reserved names (a clone's copied disks) count against both the duplicate check and the AHCI cap.
func parseDataDisks(raw, reserved []string) ([]types.DataDiskSpec, error) {
	used := make(map[string]bool, len(reserved))
	for _, n := range reserved {
		used[n] = true
	}
	specs := make([]types.DataDiskSpec, 0, len(raw))
	for _, s := range raw {
		spec, err := parseDataDiskSpec(s)
		if err != nil {
			return nil, err
		}
		specs = append(specs, spec)
	}
	// claim explicit names first, so an auto name never steals one a later spec asked for
	for _, s := range specs {
		if s.Name == "" {
			continue
		}
		if used[s.Name] {
			return nil, fmt.Errorf("data disk: name %q duplicated", s.Name)
		}
		used[s.Name] = true
	}
	autoIdx := 1
	for i := range specs {
		if specs[i].Name != "" {
			continue
		}
		for {
			candidate := fmt.Sprintf("data%d", autoIdx)
			autoIdx++
			if !used[candidate] {
				specs[i].Name, used[candidate] = candidate, true
				break
			}
		}
	}
	if total := len(reserved) + len(specs); total > maxDataDisks {
		return nil, fmt.Errorf("data disk: %d disks exceeds the %d-disk limit (macOS has no virtio-blk driver; disks ride the ich9-ahci controller's free SATA ports 0,1,3,5)", total, maxDataDisks)
	}
	return specs, nil
}

func parseDataDiskSpec(s string) (types.DataDiskSpec, error) {
	parts := strings.Split(s, ",")
	for i, part := range parts {
		key, val, ok := strings.Cut(part, "=")
		key = strings.TrimSpace(key)
		if slices.Contains(agentOnlyDiskKeys, key) {
			return types.DataDiskSpec{}, fmt.Errorf("data disk: key %q unsupported on macOS (no in-guest agent; format the disk in the guest with Disk Utility/diskutil)", key)
		}
		if ok && key == "size" {
			parts[i] = key + "=" + binarySuffix(strings.TrimSpace(val))
		}
	}
	spec, err := types.ParseDataDiskSpec(strings.Join(parts, ","))
	if err != nil {
		return spec, fmt.Errorf("data disk: %w", err)
	}
	return spec, nil
}

func createDataDisks(ctx context.Context, dir string, specs []types.DataDiskSpec) ([]string, error) {
	paths := make([]string, 0, len(specs))
	for _, s := range specs {
		path := dataDiskPath(dir, s.Name)
		if err := utils.RunQemuImg(ctx, "create", "-f", "qcow2", path, strconv.FormatInt(s.Size, 10)); err != nil {
			return nil, fmt.Errorf("create data disk %s: %w", s.Name, err)
		}
		paths = append(paths, path)
	}
	return paths, nil
}

func copyDataDisks(ctx context.Context, dir string, src []string) ([]string, error) {
	paths := make([]string, 0, len(src))
	for _, srcPath := range src {
		dst := filepath.Join(dir, filepath.Base(srcPath))
		if err := utils.ReflinkCopy(ctx, dst, srcPath, utils.Sync); err != nil {
			return nil, fmt.Errorf("copy data disk %s: %w", filepath.Base(srcPath), err)
		}
		paths = append(paths, dst)
	}
	return paths, nil
}

func dataDiskPath(dir, name string) string {
	return filepath.Join(dir, "data-"+name+".qcow2")
}

func dataDiskName(path string) string {
	return strings.TrimSuffix(strings.TrimPrefix(filepath.Base(path), "data-"), ".qcow2")
}
