package vm

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/docker/go-units"

	"github.com/cocoonstack/cocoon/types"
	"github.com/cocoonstack/cocoon/utils"
)

const (
	// minDataDiskSize mirrors cocoon's hypervisor.MinDataDiskSize, kept local because that package
	// isn't dependency-light.
	minDataDiskSize int64 = 16 << 20

	// maxDataDisks: macOS has no virtio-blk, so disks ride ich9-ahci's 6 SATA ports;
	// OpenCoreBoot=sata.2 and MacHDD=sata.4 leave exactly four free.
	maxDataDisks = 4
)

// parseDataDisks parses --data-disk args, auto-naming unnamed ones dataN; reserved names
// (a clone's copied disks) count against both the duplicate check and the AHCI cap.
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
	var spec types.DataDiskSpec
	if s == "" {
		return spec, fmt.Errorf("data disk: empty spec")
	}
	for part := range strings.SplitSeq(s, ",") {
		rawKey, rawVal, ok := strings.Cut(part, "=")
		if !ok {
			return spec, fmt.Errorf("data disk: %q is not key=value", part)
		}
		key, val := strings.TrimSpace(rawKey), strings.TrimSpace(rawVal)
		switch key {
		case "size":
			n, err := units.RAMInBytes(val)
			if err != nil {
				return spec, fmt.Errorf("data disk: invalid size %q: %w", val, err)
			}
			if n < minDataDiskSize {
				return spec, fmt.Errorf("data disk: size %s below 16MiB minimum", val)
			}
			spec.Size = n
		case "name":
			if !types.ValidDataDiskName(val) {
				return spec, fmt.Errorf("data disk: invalid name %q (must match [a-z][a-z0-9_-]{0,19}, no cocoon- prefix)", val)
			}
			spec.Name = val
		case "fstype", "mount", "directio":
			return spec, fmt.Errorf("data disk: key %q unsupported on macOS (no in-guest agent; format the disk in the guest with Disk Utility/diskutil)", key)
		default:
			return spec, fmt.Errorf("data disk: unknown key %q", key)
		}
	}
	if spec.Size == 0 {
		return spec, fmt.Errorf("data disk: size= required")
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

func copyDataDisks(dir string, src []string) ([]string, error) {
	paths := make([]string, 0, len(src))
	for _, srcPath := range src {
		dst := filepath.Join(dir, filepath.Base(srcPath))
		if err := utils.ReflinkCopy(dst, srcPath, utils.Sync); err != nil {
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
