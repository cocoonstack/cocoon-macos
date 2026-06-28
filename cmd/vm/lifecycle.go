package vm

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/projecteru2/core/log"
	"github.com/spf13/cobra"

	"github.com/cocoonstack/cocoon/utils"

	"github.com/cocoonstack/cocoon-macos/internal/home"
	"github.com/cocoonstack/cocoon-macos/qemu"
)

// Create derives a per-VM copy-on-write overlay on the golden image and writes the record,
// without booting it. It prints the VM name.
func (h *Handler) Create(cmd *cobra.Command, args []string) error {
	r, err := h.create(cmd, args[0])
	if err != nil {
		return err
	}
	fmt.Println(r.Name)
	return nil
}

// Run creates a per-VM overlay and immediately boots it, printing the name and qemu PID.
func (h *Handler) Run(cmd *cobra.Command, args []string) error {
	r, err := h.create(cmd, args[0])
	if err != nil {
		return err
	}
	if err := h.launch(cmd, home.VMDir(cmd, r.Name), r); err != nil {
		return err
	}
	fmt.Printf("%s (pid %d)\n", r.Name, r.PID)
	return nil
}

// Start boots one or more previously-created VMs, reusing each persisted TAP/netns
// across stop/start (only rm tears those down).
func (h *Handler) Start(cmd *cobra.Command, args []string) error {
	for _, n := range args {
		dir := home.VMDir(cmd, n)
		r, err := loadRec(dir)
		if err != nil {
			return err
		}
		if err := h.launch(cmd, dir, r); err != nil {
			return err
		}
		fmt.Printf("%s (pid %d)\n", n, r.PID)
	}
	return nil
}

// Stop terminates one or more running VMs. --force skips the ACPI grace window (immediate SIGKILL).
func (h *Handler) Stop(cmd *cobra.Command, args []string) error {
	grace := stopGracePeriod
	if force, _ := cmd.Flags().GetBool("force"); force {
		grace = 0
	}
	ctx := home.Ctx(cmd)
	for _, n := range args {
		dir := home.VMDir(cmd, n)
		r, err := loadRec(dir)
		if err != nil {
			return err
		}
		terminate(ctx, r, grace)
		r.PID = 0
		_ = saveRec(dir, r)
		fmt.Println(n)
	}
	return nil
}

// RM stops one or more VMs, tears down any auto-created TAP/netns (no-op for a user --tap or
// user-mode), and removes the VM's state directory.
func (h *Handler) RM(cmd *cobra.Command, args []string) error {
	for _, n := range args {
		dir := home.VMDir(cmd, n)
		if r, err := loadRec(dir); err == nil {
			terminate(home.Ctx(cmd), r, stopGracePeriod)
			teardownNet(cmd, r)
		}
		if err := os.RemoveAll(dir); err != nil {
			return fmt.Errorf("remove vm dir: %w", err)
		}
		fmt.Println(n)
	}
	return nil
}

// create is the shared worker behind Create and Run: overlay, identity, network, record.
func (h *Handler) create(cmd *cobra.Command, image string) (*record, error) {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "macos-" + time.Now().Format("20060102-150405")
	}
	oc, code, varsTmpl, err := resolveFirmware(cmd)
	if err != nil {
		return nil, err
	}
	dir := home.VMDir(cmd, name)
	if err = os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("mkdir vm dir: %w", err)
	}
	ctx := home.Ctx(cmd)
	base, digest, err := resolveBase(cmd, image, name)
	if err != nil {
		return nil, err
	}
	overlay := filepath.Join(dir, "disk.qcow2")
	if err = bakeOverlay(ctx, base, overlay); err != nil {
		return nil, err
	}
	ovmfVars := filepath.Join(dir, "OVMF_VARS.fd")
	if err = copyFile(varsTmpl, ovmfVars); err != nil {
		return nil, fmt.Errorf("copy OVMF_VARS: %w", err)
	}
	cpus, _ := cmd.Flags().GetInt("cpus")
	mem, _ := cmd.Flags().GetString("memory")
	vnc, _ := cmd.Flags().GetInt("vnc")
	ssh, _ := cmd.Flags().GetInt("ssh-port")
	vncPass, _ := cmd.Flags().GetString("vnc-password")
	netMode, _ := cmd.Flags().GetString("net")
	tap, _ := cmd.Flags().GetString("tap")
	huge, _ := cmd.Flags().GetBool("hugepages")
	r := &record{
		Name: name, Image: image, ImageDigest: digest, Disk: overlay, OpenCore: oc, OVMFCode: code, OVMFVars: ovmfVars,
		CPUs: cpus, Memory: mem, VNCDisp: vnc, SSHPort: ssh, VNCPass: vncPass, NetMode: netMode, Tap: tap, Hugepages: huge,
		VMID: newVMID(), Created: time.Now().Format(time.RFC3339),
	}
	// SMBIOS before networking: assignSMBIOS sets r.MAC = ROM, which prepareNet keeps as the guest MAC.
	if random, _ := cmd.Flags().GetBool("random-smbios"); random {
		if err = assignSMBIOS(ctx, dir, oc, r); err != nil {
			return nil, err
		}
	}
	if err = applyNet(cmd, r, tap); err != nil {
		return nil, err
	}
	return r, saveRec(dir, r)
}

// launch boots qemu for the record's spec, records the PID, and applies the VNC password (if any).
func (h *Handler) launch(cmd *cobra.Command, dir string, r *record) error {
	ctx := home.Ctx(cmd)
	logger := log.WithFunc("cmd.vm.launch")
	spec := qemu.Spec{
		Name: r.Name, Disk: r.Disk, OpenCore: r.OpenCore, OVMFCode: r.OVMFCode, OVMFVars: r.OVMFVars,
		CPUs: r.CPUs, Memory: r.Memory, VNCDisp: r.VNCDisp, SSHPort: r.SSHPort, MAC: r.MAC, VNCPass: r.VNCPass,
		Tap:       r.Tap, // set for tap/bridge/cni (a real host TAP); empty => user-mode SLIRP
		Hugepages: r.Hugepages,
		MonSock:   filepath.Join(dir, "monitor.sock"), QMPSock: filepath.Join(dir, "qmp.sock"),
	}
	pidfile := filepath.Join(dir, "qemu.pid")
	args := append(spec.Args(), "-daemonize", "-pidfile", pidfile)
	ensureNetnsLoopback(ctx, r) // CNI: a fresh netns has lo DOWN, so qemu's -vnc 127.0.0.1 would fail to bind
	if r.Netns != "" {
		logger.Debugf(ctx, "running qemu in netns %s via `ip netns exec`", filepath.Base(r.Netns))
	}
	logger.Debug(ctx, "booting macOS guest via qemu-system-x86_64 (authoritative VMM; no Go equivalent)")
	c := launchCmd(r, args) // CNI: wraps in `ip netns exec` so -netdev tap finds the in-netns TAP
	c.Stdout, c.Stderr = os.Stdout, os.Stderr
	if err := c.Run(); err != nil {
		teardownNet(cmd, r) // don't leak an auto-created TAP/netns on a failed launch
		return fmt.Errorf("launch qemu: %w", err)
	}
	if pid, err := utils.ReadPIDFile(pidfile); err == nil {
		r.PID = pid
	}
	if r.VNCPass != "" {
		if err := setVNCPassword(spec.MonSock, r.VNCPass); err != nil {
			logger.Warnf(ctx, "set vnc password: %v", err)
		}
	}
	return saveRec(dir, r)
}

// assignSMBIOS gives the VM a unique identity by injecting PlatformInfo/Generic into a per-VM
// OpenCore that is a CoW OVERLAY on the shared base loader (ocBase) — only the injected delta is
// stored per-VM, so the 19MB base is reused, not copied N times.
func assignSMBIOS(ctx context.Context, dir, ocBase string, r *record) error {
	sm, err := qemu.RandomSMBIOS()
	if err != nil {
		return err
	}
	ocOverlay := filepath.Join(dir, "OpenCore.qcow2")
	if err := bakeOverlay(ctx, ocBase, ocOverlay); err != nil {
		return err
	}
	log.WithFunc("cmd.vm.assignSMBIOS").Debugf(ctx, "injecting SMBIOS into %s via qemu-nbd", ocOverlay)
	if err := qemu.InjectSMBIOS(ocOverlay, sm); err != nil {
		return fmt.Errorf("inject SMBIOS: %w", err)
	}
	r.OpenCore, r.OpenCoreBase, r.SMBIOS, r.MAC = ocOverlay, ocBase, &sm, sm.MAC()
	return nil
}
