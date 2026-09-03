// Package home resolves the cocoon-macos state directory and its layout, shared by every subcommand.
package home

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/cocoonstack/cocoon/cmd/core"
	"github.com/cocoonstack/cocoon/images/cloudimg"
	metajson "github.com/cocoonstack/cocoon/meta/json"
)

// Default is the state root when neither --state-dir nor $COCOON_MACOS_HOME is set.
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

func VMsDir(cmd *cobra.Command) string {
	return filepath.Join(Dir(cmd), "vms")
}

// VMDir returns the per-VM state directory under VMsDir.
func VMDir(cmd *cobra.Command, name string) (string, error) {
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name {
		return "", fmt.Errorf("invalid vm name %q: must be one path component", name)
	}
	return filepath.Join(VMsDir(cmd), name), nil
}

// OpenStore opens the cloudimg store at the resolved state dir.
func OpenStore(ctx context.Context, cmd *cobra.Command) (*cloudimg.CloudImg, error) {
	metaStore, err := metajson.Open(core.ImageJSONNamespace(&cloudimg.NewConfig(Dir(cmd), 0).BaseConfig))
	if err != nil {
		return nil, fmt.Errorf("open meta store: %w", err)
	}
	s, err := cloudimg.New(ctx, Dir(cmd), 0, metaStore) // 0 = cloudimg's default pull connections
	if err != nil {
		return nil, fmt.Errorf("init cloudimg store: %w", err)
	}
	return s, nil
}
