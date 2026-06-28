package vm

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/cocoonstack/cocoon-macos/internal/home"
	"github.com/cocoonstack/cocoon-macos/qemu"
)

// Snapshot takes an offline qcow2-internal snapshot of a stopped VM (a live snapshot would corrupt
// the image, and +invtsc blocks the live-migration codepath savevm relies on).
func (h *Handler) Snapshot(cmd *cobra.Command, args []string) error {
	dir := home.VMDir(cmd, args[0])
	r, err := loadRec(dir)
	if err != nil {
		return err
	}
	if isRunning(r) {
		return fmt.Errorf("vm %q is running (pid %d); stop it first (qemu-img snapshot on a live image corrupts it)", r.Name, r.PID)
	}
	tag, _ := cmd.Flags().GetString("tag")
	if tag == "" {
		tag = "snap-" + time.Now().Format("20060102-150405")
	}
	ctx := home.Ctx(cmd)
	for _, img := range imagesToSnapshot(r) {
		if err := qemu.SnapCreate(ctx, img, tag); err != nil {
			return err
		}
	}
	r.Snapshots = append(r.Snapshots, tag)
	if err := saveRec(dir, r); err != nil {
		return err
	}
	fmt.Println(tag)
	return nil
}

// Restore reverts a VM to a snapshot tag (default: newest). A running VM is refused unless --force,
// which stops it, reverts, and relaunches.
func (h *Handler) Restore(cmd *cobra.Command, args []string) error {
	dir := home.VMDir(cmd, args[0])
	r, err := loadRec(dir)
	if err != nil {
		return err
	}
	wasRunning := isRunning(r)
	if wasRunning {
		if force, _ := cmd.Flags().GetBool("force"); !force {
			return fmt.Errorf("vm %q is running; stop it first or pass --force to stop+restore", r.Name)
		}
		terminate(home.Ctx(cmd), r, stopGracePeriod)
		r.PID = 0
	}
	tag, _ := cmd.Flags().GetString("tag")
	if tag == "" {
		if len(r.Snapshots) == 0 {
			return fmt.Errorf("vm %q has no snapshots", r.Name)
		}
		tag = r.Snapshots[len(r.Snapshots)-1]
	}
	ctx := home.Ctx(cmd)
	for _, img := range imagesToSnapshot(r) {
		if err := qemu.SnapApply(ctx, img, tag); err != nil {
			return err
		}
	}
	fmt.Println(tag)
	if wasRunning {
		return h.launch(cmd, dir, r)
	}
	return saveRec(dir, r)
}

// imagesToSnapshot lists the qcow2 images that make up a VM snapshot: the disk overlay always,
// plus OVMF_VARS only if it is qcow2 (raw .fd can't hold internal snapshots, so with a raw NVRAM
// the firmware vars do NOT roll back — only guest disk state does).
func imagesToSnapshot(r *record) []string {
	imgs := []string{r.Disk}
	if strings.HasSuffix(r.OVMFVars, ".qcow2") {
		imgs = append(imgs, r.OVMFVars)
	}
	return imgs
}
