package qemu

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"slices"

	"github.com/cocoonstack/cocoon/utils"
)

// Snapshot/restore for macOS guests is OFFLINE qcow2-internal (qemu-img snapshot), not live
// savevm: -cpu +invtsc installs a migration blocker, so a live RAM snapshot fails. The VM MUST
// be stopped before SnapCreate/SnapApply — qemu-img snapshot on a live image corrupts it.

// SnapCreate records an internal snapshot tag in the qcow2 img.
func SnapCreate(ctx context.Context, img, tag string) error {
	return utils.RunQemuImg(ctx, "snapshot", "-c", tag, img)
}

// SnapApply reverts the qcow2 img to the snapshot tag.
func SnapApply(ctx context.Context, img, tag string) error {
	return utils.RunQemuImg(ctx, "snapshot", "-a", tag, img)
}

// SnapDelete removes the snapshot tag from img (used to roll back a partial multi-disk snapshot).
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
