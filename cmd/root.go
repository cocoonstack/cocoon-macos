package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/cocoonstack/cocoon-macos/cmd/vm"
)

// Execute runs the cocoon-macos CLI.
func Execute() {
	root := &cobra.Command{
		Use:          "cocoon-macos",
		Short:        "Run full macOS (Tahoe 26) as a QEMU/KVM guest on x86 Linux",
		SilenceUsage: true,
	}
	root.AddCommand(vm.Command(vm.NewHandler()))
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
