package vm

import (
	"github.com/spf13/cobra"

	"github.com/cocoonstack/cocoon-macos/cli"
)

// Actions mirrors cocoon's cmd/vm Actions interface, trimmed to the v0.1 macOS
// surface. The cobra tree is re-declared below rather than imported from cocoon,
// whose commands.go drags in the Linux-only CH/netlink backend.
type Actions interface {
	Create(cmd *cobra.Command, args []string) error
	Run(cmd *cobra.Command, args []string) error
	Start(cmd *cobra.Command, args []string) error
	Stop(cmd *cobra.Command, args []string) error
	List(cmd *cobra.Command, args []string) error
	Inspect(cmd *cobra.Command, args []string) error
	Console(cmd *cobra.Command, args []string) error
	RM(cmd *cobra.Command, args []string) error
	Snapshot(cmd *cobra.Command, args []string) error
	Restore(cmd *cobra.Command, args []string) error
	Clone(cmd *cobra.Command, args []string) error
}

// Command builds the `vm` subcommand tree against the given handler.
func Command(h Actions) *cobra.Command {
	vmCmd := &cobra.Command{Use: "vm", Short: "Manage macOS VMs"} // --state-dir is a root persistent flag

	createCmd := &cobra.Command{
		Use:   "create [flags] IMAGE",
		Short: "Create a macOS VM from a qcow2 image (does not start it)",
		Args:  cobra.ExactArgs(1),
		RunE:  h.Create,
	}
	addVMFlags(createCmd)

	runCmd := &cobra.Command{
		Use:   "run [flags] IMAGE",
		Short: "Create and start a macOS VM from a qcow2 image",
		Args:  cobra.ExactArgs(1),
		RunE:  h.Run,
	}
	addVMFlags(runCmd)

	startCmd := &cobra.Command{
		Use:   "start VM [VM...]",
		Short: "Start created/stopped VM(s)",
		Args:  cobra.MinimumNArgs(1),
		RunE:  h.Start,
	}

	stopCmd := &cobra.Command{
		Use:   "stop VM [VM...]",
		Short: "Stop running VM(s)",
		Args:  cobra.MinimumNArgs(1),
		RunE:  h.Stop,
	}
	stopCmd.Flags().Bool("force", false, "force stop (immediate SIGKILL, skip ACPI shutdown)")

	listCmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List VMs with status",
		RunE:    h.List,
	}
	cli.AddFormatFlag(listCmd)

	inspectCmd := &cobra.Command{
		Use:   "inspect VM",
		Short: "Show detailed VM info (JSON)",
		Args:  cobra.ExactArgs(1),
		RunE:  h.Inspect,
	}

	consoleCmd := &cobra.Command{
		Use:   "console VM",
		Short: "Attach to a running VM's console (VNC/serial)",
		Args:  cobra.ExactArgs(1),
		RunE:  h.Console,
	}

	rmCmd := &cobra.Command{
		Use:   "rm VM [VM...]",
		Short: "Remove VM(s): stop if running, tear down networking, delete state",
		Args:  cobra.MinimumNArgs(1),
		RunE:  h.RM,
	}
	rmCmd.Flags().Bool("force", false, "force kill (immediate SIGKILL, skip the ACPI grace window)")

	snapshotCmd := &cobra.Command{
		Use:   "snapshot VM",
		Short: "Take an offline qcow2-internal snapshot of a stopped VM (disk state; NVRAM if qcow2)",
		Args:  cobra.ExactArgs(1),
		RunE:  h.Snapshot,
	}
	snapshotCmd.Flags().String("tag", "", "snapshot tag (default: snap-<timestamp>)")

	restoreCmd := &cobra.Command{
		Use:   "restore VM",
		Short: "Revert a VM to a snapshot (default: newest)",
		Args:  cobra.ExactArgs(1),
		RunE:  h.Restore,
	}
	restoreCmd.Flags().String("tag", "", "snapshot tag to restore (default: newest)")
	restoreCmd.Flags().Bool("force", false, "stop the VM, restore, then relaunch if it was running")

	cloneCmd := &cobra.Command{
		Use:   "clone SRC",
		Short: "Clone a VM: fresh CoW overlay on the shared base + a unique Apple identity + its own TAP",
		Args:  cobra.ExactArgs(1),
		RunE:  h.Clone,
	}
	addVMFlags(cloneCmd) // loader is inherited from SRC

	vmCmd.AddCommand(createCmd, runCmd, startCmd, stopCmd, listCmd, inspectCmd, consoleCmd, rmCmd,
		snapshotCmd, restoreCmd, cloneCmd)
	return vmCmd
}

func addVMFlags(cmd *cobra.Command) {
	cmd.Flags().StringP("name", "n", "", "VM name (default: generated)")
	cmd.Flags().Int("cpus", 4, "vCPU count")
	cmd.Flags().String("memory", "8192", "guest memory in MiB")
	cmd.Flags().Bool("hugepages", false, "back guest RAM with 2 MiB hugepages (needs host hugepages reserved; lower TLB/EPT overhead)")
	cmd.Flags().StringArray("data-disk", nil, "attach an extra qcow2 data disk: comma-separated key=value (size= required e.g. size=20G; name= optional, default dataN). Repeatable, max 4. macOS has no in-guest agent, so fstype=/mount= are unsupported — format it in the guest (Disk Utility/diskutil)")
	cmd.Flags().Int("vnc", -1, "VNC display number (n => host 127.0.0.1:590n); <0 disables")
	cmd.Flags().Int("ssh-port", 0, "host port forwarded to guest :22; 0 disables")
	cmd.Flags().String("opencore", "", "OpenCore.qcow2 boot loader (default: <state-dir>/firmware/OpenCore.qcow2; provisioned by scripts/doctor.sh)")
	cmd.Flags().String("ovmf-code", "", "OVMF_CODE firmware (default: <state-dir>/firmware/OVMF_CODE.fd)")
	cmd.Flags().String("ovmf-vars", "", "OVMF_VARS template, copied per-VM (default: <state-dir>/firmware/OVMF_VARS.fd)")
	cmd.Flags().Bool("random-smbios", false, "inject a unique Apple SMBIOS identity per VM (serial/MLB/UUID/ROM)")
	cmd.Flags().String("vnc-password", "", "set a VNC password (≤8 chars) so macOS Screen Sharing can connect (QEMU password auth)")
	cmd.Flags().String("net", "user", "network mode: user (SLIRP + --ssh-port hostfwd) | tap | cni | bridge (cocoon auto-creates the TAP, Linux only)")
	cmd.Flags().String("tap", "", "pre-created host TAP ifname (skips auto-create; e.g. an existing bridge port / cocoon CNI tap)")
	cmd.Flags().String("bridge", "", "existing Linux bridge to enslave the auto-created TAP to (--net tap|bridge)")
	cmd.Flags().String("cni-conf-dir", "", "CNI config dir for --net cni (default /etc/cni/net.d)")
	cmd.Flags().String("cni-bin-dir", "", "CNI plugin dir for --net cni (default /opt/cni/bin)")
}
