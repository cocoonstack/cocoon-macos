// Package home resolves the cocoon-macos state directory shared by every subcommand.
package home

import (
	"os"

	"github.com/spf13/cobra"
)

// Default is the state root used when neither --state-dir nor $COCOON_MACOS_HOME is set
// (mirrors cocoon's /var/lib/cocoon).
const Default = "/var/lib/cocoon-macos"

// Dir resolves the state root: --state-dir wins, else $COCOON_MACOS_HOME, else Default.
func Dir(cmd *cobra.Command) string {
	if d, _ := cmd.Flags().GetString("state-dir"); d != "" {
		return d
	}
	if d := os.Getenv("COCOON_MACOS_HOME"); d != "" {
		return d
	}
	return Default
}
