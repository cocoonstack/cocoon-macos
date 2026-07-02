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
	// minDataDiskSize mirrors cocoon's hypervisor.MinDataDiskSize (16 MiB); a local const because
	// that package drags in storage/metering/backend (net/http, os/exec) and isn't dependency-light.
	minDataDiskSize int64 = 16 << 20

	// maxDataDisks caps data disks at 4. WHY: macOS has no virtio-blk driver (see qemu/launch.go),
	// so disks ride the single ich9-ahci controller's 6 SATA ports; OpenCoreBoot=sata.2 and
	// MacHDD=sata.4 leave ports 0,1,3,5 — exactly four free.
	maxDataDisks = 4
)

// parseDataDisks parses every --data-disk arg into a DataDiskSpec, fills default names
// (data0, data1, …), rejects duplicate names, and caps the total at maxDataDisks. reserved names
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
	autoIdx := 0
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

// parseDataDiskSpec parses one comma-separated --data-disk arg. size= is required (≥16MiB); name= is
// optional. fstype=/mount=/directio= are rejected: macOS guests have no cloud-init/agent to format
// or mount a disk (cocoon's Linux path does) — the disk is attached raw to be formatted in-guest.
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
			// intentional macOS divergence from cocoon: no in-guest agent to format/mount a disk, so the
			// disk is attached raw — format it in the guest with Disk Utility/diskutil.
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

// createDataDisks provisions each parsed spec as an empty qcow2 under dir and returns their paths.
func createDataDisks(ctx context.Context, dir string, specs []types.DataDiskSpec) ([]string, error) {
	paths := make([]string, 0, len(specs))
	for _, s := range specs {
		path := dataDiskPath(dir, s.Name)
		// size as a plain byte count; qemu-img create takes an integer size in bytes
		if err := utils.RunQemuImg(ctx, "create", "-f", "qcow2", path, strconv.FormatInt(s.Size, 10)); err != nil {
			return nil, fmt.Errorf("create data disk %s: %w", s.Name, err)
		}
		paths = append(paths, path)
	}
	return paths, nil
}

// copyDataDisks reflink-copies SRC's data disks into dir keeping their basenames, so both the disk
// names and their qcow2 contents carry over to a clone; it returns the new paths.
func copyDataDisks(dir string, src []string) ([]string, error) {
	paths := make([]string, 0, len(src))
	for _, srcPath := range src {
		dst := filepath.Join(dir, filepath.Base(srcPath))
		if err := utils.ReflinkCopy(dst, srcPath); err != nil {
			return nil, fmt.Errorf("copy data disk %s: %w", filepath.Base(srcPath), err)
		}
		paths = append(paths, dst)
	}
	return paths, nil
}

// dataDiskPath is the on-disk path of a data disk given the VM dir and disk name.
func dataDiskPath(dir, name string) string {
	return filepath.Join(dir, "data-"+name+".qcow2")
}

// dataDiskName recovers a data disk's name from its dataDiskPath (data-<name>.qcow2).
func dataDiskName(path string) string {
	return strings.TrimSuffix(strings.TrimPrefix(filepath.Base(path), "data-"), ".qcow2")
}
