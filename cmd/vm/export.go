package vm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	cmdimage "github.com/cocoonstack/cocoon-macos/cmd/image"
	"github.com/cocoonstack/cocoon-macos/home"
	"github.com/cocoonstack/cocoon/cmd/cliutil"
	"github.com/cocoonstack/cocoon/progress"
	"github.com/cocoonstack/cocoon/utils"
)

const exportRestartTimeout = 2 * time.Minute

func (h *Handler) Export(cmd *cobra.Command, args []string) error {
	ctx := cliutil.CommandContext(cmd)
	vmName, destination := args[0], args[1]
	if err := cmdimage.ValidatePushReference(destination); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(home.Dir(cmd), ".cocoon-macos-export-*.qcow2")
	if err != nil {
		return fmt.Errorf("create export temp file: %w", err)
	}
	tmpPath := tmp.Name()
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close export temp file: %w", err)
	}
	defer os.Remove(tmpPath) //nolint:errcheck

	dir := home.VMDir(cmd, vmName)
	if err := withVMLock(ctx, dir, func() (retErr error) {
		r, err := loadRec(dir)
		if err != nil {
			return err
		}
		wasRunning := isRunning(r)
		restartVNC, restartVNCPass := exportRestartVNC(cmd, r)
		if wasRunning {
			if err := requireCNIVNCPassword(r.Netns != "", restartVNC, restartVNCPass); err != nil {
				return fmt.Errorf("restart settings: %w", err)
			}
			terminate(ctx, r, stopGracePeriod)
			if isRunning(r) {
				return fmt.Errorf("VM %s is still running after shutdown", vmName)
			}
			quiesceNet(cmd, r)
			stopVNCProxy(ctx, dir)
			r.PID, r.VNCDisp, r.VNCPass = 0, -1, ""
			defer func() {
				restartCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), exportRestartTimeout)
				defer cancel()
				restartCmd := *cmd
				restartCmd.SetContext(restartCtx)
				r.VNCDisp, r.VNCPass = restartVNC, restartVNCPass
				restartErr := h.launch(&restartCmd, dir, r)
				if isRunning(r) {
					unquiesceNet(&restartCmd, r)
				}
				retErr = errors.Join(retErr, restartErr)
			}()
			if err := saveRec(dir, r); err != nil {
				return err
			}
		}

		return utils.RunQemuImg(ctx, "convert", "-p", "-f", "qcow2", "-O", "qcow2", "-c", r.Disk, tmpPath)
	}); err != nil {
		return fmt.Errorf("export VM %s: %w", vmName, err)
	}

	desc, err := cmdimage.PushCloudImage(ctx, destination, tmpPath, map[string]string{
		"cocoonstack.os.name":   "macos",
		"cocoonstack.source.vm": vmName,
	})
	if err != nil {
		return fmt.Errorf("push cloud image: %w", err)
	}
	localName, _ := cmd.Flags().GetString("local-name")
	if localName != "" {
		_, store, openErr := home.OpenStore(cmd)
		if openErr != nil {
			return fmt.Errorf("pushed %s as %s, but opening the local cloud-image store failed: %w", destination, desc.Digest, openErr)
		}
		if importErr := store.Import(ctx, localName, progress.Nop, tmpPath); importErr != nil {
			return fmt.Errorf("pushed %s as %s, but retaining local cloud image %s failed: %w", destination, desc.Digest, localName, importErr)
		}
	}
	fmt.Println(desc.Digest)
	return nil
}

func exportRestartVNC(cmd *cobra.Command, r *record) (int, string) {
	vnc, password := r.VNCDisp, r.VNCPass
	if cmd.Flags().Changed("vnc") {
		vnc, _ = cmd.Flags().GetInt("vnc")
	}
	if cmd.Flags().Changed("vnc-password") {
		password, _ = cmd.Flags().GetString("vnc-password")
	}
	return vnc, password
}
