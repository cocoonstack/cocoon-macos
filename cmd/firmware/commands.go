package firmware

import "github.com/spf13/cobra"

// Actions manages the shared OpenCore/OVMF assets under <state-dir>/firmware that vm create/run
// default to (so the loader/firmware live once and are reused across every VM).
type Actions interface {
	Install(cmd *cobra.Command, args []string) error
	List(cmd *cobra.Command, args []string) error
}

// Command builds the `firmware` subcommand tree.
func Command(h Actions) *cobra.Command {
	fwCmd := &cobra.Command{Use: "firmware", Short: "Manage shared OpenCore/OVMF firmware under <state-dir>/firmware"}
	fwCmd.PersistentFlags().String("state-dir", "", "VM state root (default $COCOON_MACOS_HOME or /var/lib/cocoon-macos)")

	installCmd := &cobra.Command{
		Use:   "install",
		Short: "Copy the OpenCore loader + OVMF firmware into the shared store so vm run/create default to them",
		RunE:  h.Install,
	}
	installCmd.Flags().String("opencore", "", "OpenCore.qcow2 boot loader -> firmware/OpenCore.qcow2")
	installCmd.Flags().String("ovmf-code", "", "OVMF_CODE firmware -> firmware/OVMF_CODE.fd")
	installCmd.Flags().String("ovmf-vars", "", "OVMF_VARS NVRAM template -> firmware/OVMF_VARS.fd")

	listCmd := &cobra.Command{Use: "list", Aliases: []string{"ls"}, Short: "Show installed shared firmware", RunE: h.List}

	fwCmd.AddCommand(installCmd, listCmd)
	return fwCmd
}
