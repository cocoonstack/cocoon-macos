package vm

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/cocoonstack/cocoon-macos/internal/home"
)

// Clone seeds a new VM from SRC's disk state via a fresh CoW overlay on the SAME immutable base,
// with a unique Apple identity (cold boot re-runs OpenCore PlatformInfo) and its own TAP — so two
// clones never share a serial/MAC (App Store ban risk). Network/VNC/SSH come from flags so a clone
// doesn't collide on the source's host ports; CPUs/Memory/loader are inherited unless overridden.
func (h *Handler) Clone(cmd *cobra.Command, args []string) error {
	src := args[0]
	srcRec, err := loadRec(home.VMDir(cmd, src))
	if err != nil {
		return err
	}
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = src + "-clone-" + time.Now().Format("150405")
	}
	dir := home.VMDir(cmd, name)
	if err = os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("mkdir vm dir: %w", err)
	}
	ctx := home.Ctx(cmd)
	base, digest, err := resolveBase(cmd, srcRec.Image, name)
	if err != nil {
		return err
	}
	overlay := filepath.Join(dir, "disk.qcow2")
	if err = bakeOverlay(ctx, base, overlay); err != nil {
		return err
	}
	ovmfVars := filepath.Join(dir, filepath.Base(srcRec.OVMFVars))
	if err = copyFile(srcRec.OVMFVars, ovmfVars); err != nil {
		return fmt.Errorf("copy OVMF_VARS: %w", err)
	}
	r := &record{
		Name: name, Image: srcRec.Image, ImageDigest: digest, Disk: overlay,
		OVMFCode: srcRec.OVMFCode, OVMFVars: ovmfVars, CPUs: srcRec.CPUs, Memory: srcRec.Memory,
		VMID: newVMID(), Created: time.Now().Format(time.RFC3339),
	}
	if cmd.Flags().Changed("cpus") {
		r.CPUs, _ = cmd.Flags().GetInt("cpus")
	}
	if cmd.Flags().Changed("memory") {
		r.Memory, _ = cmd.Flags().GetString("memory")
	}
	r.Hugepages = srcRec.Hugepages
	if cmd.Flags().Changed("hugepages") {
		r.Hugepages, _ = cmd.Flags().GetBool("hugepages")
	}
	r.VNCDisp, _ = cmd.Flags().GetInt("vnc")
	r.SSHPort, _ = cmd.Flags().GetInt("ssh-port")
	r.VNCPass, _ = cmd.Flags().GetString("vnc-password")
	// fresh identity when SRC has one or --random-smbios (cold boot re-reads PlatformInfo from the overlay)
	random, _ := cmd.Flags().GetBool("random-smbios")
	freshIdentity := random || srcRec.SMBIOS != nil
	// overlay the recorded base, never SRC's per-VM overlay (would break on `vm rm SRC`)
	ocBase, baseErr := cloneOpenCoreBase(cmd, srcRec)
	if baseErr != nil {
		return baseErr
	}
	if err = prepareOpenCore(ctx, dir, ocBase, freshIdentity, r); err != nil {
		return err
	}
	if !freshIdentity {
		r.MAC = srcRec.MAC // no fresh identity: inherit SRC's MAC
	}
	r.NetMode, _ = cmd.Flags().GetString("net")
	tapFlag, _ := cmd.Flags().GetString("tap")
	r.Tap = tapFlag
	if err = applyNet(cmd, r, tapFlag); err != nil {
		return err
	}
	if err := saveRec(dir, r); err != nil {
		return err
	}
	fmt.Println(name)
	return nil
}

// cloneOpenCoreBase returns the immutable base a clone's fresh identity overlays: SRC's recorded
// base (inherits a custom --opencore), else SRC's OpenCore when it is itself the base, else the
// shared firmware (legacy records). Never SRC's per-VM overlay.
func cloneOpenCoreBase(cmd *cobra.Command, src *record) (string, error) {
	switch {
	case src.OpenCoreBase != "":
		return src.OpenCoreBase, nil
	case src.SMBIOS == nil:
		return src.OpenCore, nil
	default:
		oc, _, _, err := resolveFirmware(cmd)
		return oc, err
	}
}
