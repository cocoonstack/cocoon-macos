package qemu

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"slices"

	"github.com/cocoonstack/cocoon/utils"
)

// SnapCreate records an internal snapshot tag in the qcow2 img. Offline only: +invtsc blocks live savevm, and qemu-img snapshot on a live image corrupts it — the VM must be stopped.
func SnapCreate(ctx context.Context, img, tag string) error {
	return utils.RunQemuImg(ctx, "snapshot", "-c", tag, img)
}

func SnapApply(ctx context.Context, img, tag string) error {
	return utils.RunQemuImg(ctx, "snapshot", "-a", tag, img)
}

func SnapDelete(ctx context.Context, img, tag string) error {
	return utils.RunQemuImg(ctx, "snapshot", "-d", tag, img)
}

type snapshotEntry struct {
	Name string `json:"name"`
}

// SnapExists reports whether img carries the snapshot tag.
func SnapExists(ctx context.Context, img, tag string) (bool, error) {
	out, err := exec.CommandContext(ctx, "qemu-img", "info", "--output=json", img).Output() //nolint:gosec // img is an internal path
	if err != nil {
		return false, fmt.Errorf("qemu-img info %s: %w", img, err)
	}
	var info struct {
		Snapshots []snapshotEntry `json:"snapshots"`
	}
	if err := json.Unmarshal(out, &info); err != nil {
		return false, fmt.Errorf("parse qemu-img info %s: %w", img, err)
	}
	return slices.ContainsFunc(info.Snapshots, func(s snapshotEntry) bool { return s.Name == tag }), nil
}
