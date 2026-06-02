package qemu

import (
	"context"

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
