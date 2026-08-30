package vm

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/cocoonstack/cocoon-macos/home"
	"github.com/cocoonstack/cocoon/cmd/cliutil"
	"github.com/cocoonstack/cocoon/utils"
)

func (h *Handler) Clone(cmd *cobra.Command, args []string) error {
	src := args[0]
	srcRec, err := loadRec(home.VMDir(cmd, src))
	if err != nil {
		return err
	}
	name := requestedVMName(cmd, src+"-clone-"+time.Now().Format("150405"))
	return withVMLock(cliutil.CommandContext(cmd), home.VMDir(cmd, name), func() error {
		return h.clone(cmd, srcRec, name)
	})
}

func (h *Handler) clone(cmd *cobra.Command, srcRec *record, name string) (retErr error) {
	netMode, _ := cmd.Flags().GetString("net")
	vnc, _ := cmd.Flags().GetInt("vnc")
	vncPass, _ := cmd.Flags().GetString("vnc-password")
	if err := requireCNIVNCPassword(netMode == netCNI, vnc, vncPass); err != nil {
		return err
	}
	cpus := srcRec.CPUs
	if cmd.Flags().Changed("cpus") {
		cpus, _ = cmd.Flags().GetInt("cpus")
	}
	if err := validateMacOSCPUs(cpus); err != nil {
		return err
	}
	// SRC's disk names are reserved: extra --data-disk specs must not collide and the combined count still honors the AHCI cap
	reserved := make([]string, len(srcRec.DataDisks))
	for i, p := range srcRec.DataDisks {
		reserved[i] = dataDiskName(p)
	}
	rawDisks, _ := cmd.Flags().GetStringArray("data-disk")
	diskSpecs, err := parseDataDisks(rawDisks, reserved)
	if err != nil {
		return err
	}
	storage := srcRec.Storage
	if cmd.Flags().Changed("storage") {
		storage, err = storageFromFlag(cmd)
		if err != nil {
			return err
		}
	}
	dir, overlay, ovmfVars, digest, err := scaffoldVM(cmd, name, srcRec.Image, srcRec.OVMFVars, filepath.Base(srcRec.OVMFVars))
	if err != nil {
		return err
	}
	var r *record
	defer func() {
		if retErr != nil {
			retErr = errors.Join(retErr, cleanupFailedVM(cmd, dir, r))
		}
	}()
	ctx := cliutil.CommandContext(cmd)
	storage, err = resizeSystemDisk(ctx, overlay, storage)
	if err != nil {
		_ = os.RemoveAll(dir)
		return err
	}
	copied, err := copyDataDisks(dir, srcRec.DataDisks)
	if err != nil {
		return err
	}
	newDisks, err := createDataDisks(ctx, dir, diskSpecs)
	if err != nil {
		return err
	}
	r = &record{
		Name: name, Image: srcRec.Image, ImageDigest: digest, Disk: overlay,
		OVMFCode: srcRec.OVMFCode, OVMFVars: ovmfVars, CPUs: cpus, Memory: srcRec.Memory, Storage: storage,
		DataDisks: append(copied, newDisks...),
		VMID:      utils.GenerateID(), Created: time.Now().Format(time.RFC3339),
	}
	if cmd.Flags().Changed("memory") {
		r.Memory, _ = cmd.Flags().GetString("memory")
	}
	r.Hugepages = srcRec.Hugepages
	if cmd.Flags().Changed("hugepages") {
		r.Hugepages, _ = cmd.Flags().GetBool("hugepages")
	}
	r.ExitOnReboot = srcRec.ExitOnReboot
	if cmd.Flags().Changed("exit-on-reboot") {
		r.ExitOnReboot, _ = cmd.Flags().GetBool("exit-on-reboot")
	}
	r.VNCDisp = vnc
	r.SSHPort, _ = cmd.Flags().GetInt("ssh-port")
	r.VNCPass = vncPass
	// fresh identity when SRC has one or --random-smbios, so two clones never share a serial/MAC
	randomSMBIOS, _ := cmd.Flags().GetBool("random-smbios")
	freshIdentity := randomSMBIOS || srcRec.SMBIOS != nil
	ocBase, err := cloneOpenCoreBase(cmd, srcRec)
	if err != nil {
		return err
	}
	if err = prepareOpenCore(ctx, dir, ocBase, freshIdentity, r); err != nil {
		return err
	}
	if !freshIdentity {
		r.MAC = srcRec.MAC // prepareOpenCore only sets a fresh MAC for fresh identities
	}
	r.NetMode = netMode
	tapFlag, _ := cmd.Flags().GetString("tap")
	r.Tap = tapFlag
	if err = applyNet(cmd, r); err != nil {
		return err
	}
	if err := saveRec(dir, r); err != nil {
		return err
	}
	fmt.Println(name)
	return nil
}

// cloneOpenCoreBase returns the immutable base a clone overlays — never SRC's per-VM overlay, which would break on `vm rm SRC`.
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
