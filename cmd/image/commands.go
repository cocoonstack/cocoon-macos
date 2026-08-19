package image

import (
	"github.com/spf13/cobra"

	"github.com/cocoonstack/cocoon/cmd/cliutil"
)

// Actions is the image-subcommand surface, backed by cocoon's cloudimg store.
type Actions interface {
	Pull(cmd *cobra.Command, args []string) error
	Import(cmd *cobra.Command, args []string) error
	Export(cmd *cobra.Command, args []string) error
	List(cmd *cobra.Command, args []string) error
	Inspect(cmd *cobra.Command, args []string) error
	RM(cmd *cobra.Command, args []string) error
}

// Command builds the `image` subcommand tree against the given handler.
func Command(h Actions) *cobra.Command {
	imageCmd := &cobra.Command{Use: "image", Short: "Manage macOS disk images (reuses cocoon's cloudimg store)"} // --state-dir is a root persistent flag

	pull := &cobra.Command{
		Use:   "pull REF",
		Short: "Pull a macOS qcow2 into the store (http(s) URL via cloudimg, or an OCI/ghcr ref via oras-go)",
		Args:  cobra.ExactArgs(1),
		RunE:  h.Pull,
	}
	pull.Flags().Bool("force", false, "re-pull even if already present")
	importCmd := &cobra.Command{
		Use:   "import NAME [FILE]",
		Short: "Import a qcow2 cloud image from a file or stdin",
		Args:  cobra.RangeArgs(1, 2),
		RunE:  h.Import,
	}
	exportCmd := &cobra.Command{
		Use:   "export IMAGE",
		Short: "Export a locally stored cloud image as qcow2",
		Args:  cobra.ExactArgs(1),
		RunE:  h.Export,
	}
	exportCmd.Flags().StringP("output", "o", "", "output file path (default: <image>.qcow2; use - for stdout)")

	list := &cobra.Command{Use: "list", Aliases: []string{"ls"}, Short: "List images", RunE: h.List}
	cliutil.AddFormatFlag(list)
	inspect := &cobra.Command{Use: "inspect REF", Short: "Show one image (JSON)", Args: cobra.ExactArgs(1), RunE: h.Inspect}
	rm := &cobra.Command{Use: "rm REF [REF...]", Short: "Remove image(s)", Args: cobra.MinimumNArgs(1), RunE: h.RM}

	imageCmd.AddCommand(pull, importCmd, exportCmd, list, inspect, rm)
	return imageCmd
}
