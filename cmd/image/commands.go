package image

import (
	"github.com/spf13/cobra"

	"github.com/cocoonstack/cocoon/cmd/cliutil"
)

// Command builds the `image` subcommand tree against the given handler.
func Command(h *Handler) *cobra.Command {
	imageCmd := &cobra.Command{Use: "image", Short: "Manage macOS disk images (reuses cocoon's cloudimg store)"} // --state-dir is a root persistent flag

	pull := &cobra.Command{
		Use:   "pull REF",
		Short: "Pull a macOS qcow2 into the store (http(s) URL via cloudimg, or an OCI/ghcr ref via oras-go)",
		Args:  cobra.ExactArgs(1),
		RunE:  h.Pull,
	}
	pull.Flags().Bool("force", false, "re-pull even if already present")

	list := &cobra.Command{Use: "list", Aliases: []string{"ls"}, Short: "List images", RunE: h.List}
	cliutil.AddFormatFlag(list)
	inspect := &cobra.Command{Use: "inspect REF", Short: "Show one image (JSON)", Args: cobra.ExactArgs(1), RunE: h.Inspect}
	rm := &cobra.Command{Use: "rm REF [REF...]", Short: "Remove image(s)", Args: cobra.MinimumNArgs(1), RunE: h.RM}

	imageCmd.AddCommand(pull, list, inspect, rm)
	return imageCmd
}
