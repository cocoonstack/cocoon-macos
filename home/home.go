// Package home resolves the cocoon-macos state directory and its layout, shared by every subcommand.
package home

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/cocoonstack/cocoon/cmd/cliutil"
	"github.com/cocoonstack/cocoon/images/cloudimg"
)

// Default is the state root used when neither --state-dir nor $COCOON_MACOS_HOME is set
// (mirrors cocoon's /var/lib/cocoon).
const Default = "/var/lib/cocoon-macos"

// Dir resolves the state root: --state-dir wins, else $COCOON_MACOS_HOME, else Default.
func Dir(cmd *cobra.Command) string {
	d, _ := cmd.Flags().GetString("state-dir")
	return cmp.Or(d, os.Getenv("COCOON_MACOS_HOME"), Default)
}

// FirmwareDir is the shared loader/firmware directory under the state root.
func FirmwareDir(cmd *cobra.Command) string {
	return filepath.Join(Dir(cmd), "firmware")
}

// VMsDir is the directory holding every per-VM state directory.
func VMsDir(cmd *cobra.Command) string {
	return filepath.Join(Dir(cmd), "vms")
}

// VMDir is the per-VM state directory under VMsDir.
func VMDir(cmd *cobra.Command, name string) string {
	return filepath.Join(VMsDir(cmd), name)
}

// OpenStore opens the shared cloudimg store rooted at the resolved state dir, returning the
// command context alongside it.
func OpenStore(cmd *cobra.Command) (context.Context, *cloudimg.CloudImg, error) {
	ctx := cliutil.CommandContext(cmd)
	s, err := cloudimg.New(ctx, Dir(cmd), 0) // 0 = cloudimg's default pull connections
	if err != nil {
		return ctx, nil, fmt.Errorf("init cloudimg store: %w", err)
	}
	return ctx, s, nil
}
