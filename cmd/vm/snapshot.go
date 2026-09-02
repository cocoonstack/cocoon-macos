package vm

import (
	"context"
	"fmt"
	"time"

	"github.com/projecteru2/core/log"
	"github.com/spf13/cobra"

	"github.com/cocoonstack/cocoon-macos/home"
	"github.com/cocoonstack/cocoon-macos/qemu"
	"github.com/cocoonstack/cocoon/cmd/cliutil"
)

func (h *Handler) Snapshot(cmd *cobra.Command, args []string) error {
	dir, err := home.VMDir(cmd, args[0])
	if err != nil {
		return err
	}
	ctx := cliutil.CommandContext(cmd)
	tag, _ := cmd.Flags().GetString("tag")
	if tag == "" {
		tag = "snap-" + time.Now().Format("20060102-150405")
	}
	if err := withVMLock(ctx, dir, func() error {
		r, err := loadRec(dir)
		if err != nil {
			return err
		}
		running, err := reconcileRunningQEMU(dir, r)
		if err != nil {
			return err
		}
		if running {
			return fmt.Errorf("vm %q is running (pid %d); stop it first (qemu-img snapshot on a live image corrupts it)", r.Name, r.PID)
		}
		if err := snapshotAllOrNothing(ctx, imagesToSnapshot(r), tag); err != nil {
			return err
		}
		r.Snapshots = append(r.Snapshots, tag)
		return saveRec(dir, r)
	}); err != nil {
		return err
	}
	fmt.Println(tag)
	return nil
}

func (h *Handler) Restore(cmd *cobra.Command, args []string) error {
	dir, err := home.VMDir(cmd, args[0])
	if err != nil {
		return err
	}
	ctx := cliutil.CommandContext(cmd)
	var tag string
	if err := withVMLock(ctx, dir, func() error {
		r, err := loadRec(dir)
		if err != nil {
			return err
		}
		wasRunning, err := reconcileRunningQEMU(dir, r)
		if err != nil {
			return err
		}
		if wasRunning {
			if force, _ := cmd.Flags().GetBool("force"); !force {
				return fmt.Errorf("vm %q is running; stop it first or pass --force to stop+restore", r.Name)
			}
			vncPass, _ := cmd.Flags().GetString("vnc-password")
			if err := validateVNCPassword(vncPass); err != nil {
				return err
			}
			if vncPass == "" && r.VNCDisp >= 0 && (r.VNCPassSet || r.Netns != "") {
				return fmt.Errorf("vm %q serves a password-gated VNC display; pass --vnc-password to relaunch it, or stop, restore, then start --vnc %d --vnc-password", r.Name, r.VNCDisp)
			}
			r.VNCPass = vncPass
			terminate(ctx, r, stopGracePeriod)
			r.PID = 0
			if err := saveRec(dir, r); err != nil {
				return err
			}
		}
		tag, _ = cmd.Flags().GetString("tag")
		if tag == "" {
			if len(r.Snapshots) == 0 {
				return fmt.Errorf("vm %q has no snapshots", r.Name)
			}
			tag = r.Snapshots[len(r.Snapshots)-1]
		}
		imgs := imagesToSnapshot(r)
		// apply reverts in place and can't be undone; confirm every disk carries the tag first
		for _, img := range imgs {
			ok, err := qemu.SnapExists(ctx, img, tag)
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("snapshot %q missing on disk %s; refusing a partial restore", tag, img)
			}
		}
		for _, img := range imgs {
			if err := qemu.SnapApply(ctx, img, tag); err != nil {
				return fmt.Errorf("restore %q on %s failed mid-apply; disks may be inconsistent: %w", tag, img, err)
			}
		}
		if wasRunning {
			return h.launch(cmd, dir, r)
		}
		return saveRec(dir, r)
	}); err != nil {
		return err
	}
	fmt.Println(tag)
	return nil
}

// snapshotAllOrNothing tags every image or rolls back the ones already tagged, so a partial failure never leaves an untracked snapshot point on some disks.
func snapshotAllOrNothing(ctx context.Context, imgs []string, tag string) error {
	var created []string
	for _, img := range imgs {
		if err := qemu.SnapCreate(ctx, img, tag); err != nil {
			for _, done := range created {
				if rbErr := qemu.SnapDelete(ctx, done, tag); rbErr != nil {
					log.WithFunc("cmd.vm.snapshotAllOrNothing").Warnf(ctx, "rollback snapshot %q on %s: %v", tag, done, rbErr)
				}
			}
			return fmt.Errorf("snapshot %s: %w", img, err)
		}
		created = append(created, img)
	}
	return nil
}

// imagesToSnapshot includes OVMF_VARS only when qcow2 — a raw .fd can't hold internal snapshots, so with raw NVRAM only guest disk state rolls back.
func imagesToSnapshot(r *record) []string {
	imgs := []string{r.Disk}
	if qemu.IsQcow2NVRAM(r.OVMFVars) {
		imgs = append(imgs, r.OVMFVars)
	}
	return append(imgs, r.DataDisks...)
}
