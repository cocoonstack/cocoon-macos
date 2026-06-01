package vm

import "github.com/spf13/cobra"

// Actions mirrors cocoon's cmd/vm Actions interface, trimmed to the v0.1 macOS
// surface. The cobra tree below is copied from cocoon (its commands.go depends
// only on cobra, but importing the whole package drags the Linux-only CH/netlink
// backend, so we re-declare the tree here and import just cocoon's neutral types).
type Actions interface {
	Create(cmd *cobra.Command, args []string) error
	Run(cmd *cobra.Command, args []string) error
	Start(cmd *cobra.Command, args []string) error
	Stop(cmd *cobra.Command, args []string) error
	List(cmd *cobra.Command, args []string) error
	Inspect(cmd *cobra.Command, args []string) error
	Console(cmd *cobra.Command, args []string) error
	RM(cmd *cobra.Command, args []string) error
}

// Command builds the `vm` subcommand tree against the given handler.
func Command(h Actions) *cobra.Command {
	vmCmd := &cobra.Command{Use: "vm", Short: "Manage macOS VMs"}

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
		Short: "Remove created/stopped VM(s)",
		Args:  cobra.MinimumNArgs(1),
		RunE:  h.RM,
	}

	vmCmd.AddCommand(createCmd, runCmd, startCmd, stopCmd, listCmd, inspectCmd, consoleCmd, rmCmd)
	return vmCmd
}

func addVMFlags(cmd *cobra.Command) {
	cmd.Flags().StringP("name", "n", "", "VM name (default: generated)")
	cmd.Flags().Int("cpus", 4, "vCPU count")
	cmd.Flags().String("memory", "8192", "guest memory in MiB")
	cmd.Flags().Int("vnc", -1, "VNC display number (n => host 127.0.0.1:590n); <0 disables")
	cmd.Flags().Int("ssh-port", 0, "host port forwarded to guest :22; 0 disables")
	cmd.Flags().String("opencore", "", "OpenCore.qcow2 boot loader (required)")
	cmd.Flags().String("ovmf-code", "", "OVMF_CODE firmware (required)")
	cmd.Flags().String("ovmf-vars", "", "OVMF_VARS template (copied per-VM)")
	cmd.Flags().String("state-dir", "", "VM state root (default $COCOON_MACOS_HOME or ~/.cocoon-macos)")
}
