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
	"github.com/cocoonstack/cocoon/images"
	"github.com/cocoonstack/cocoon/images/cloudimg"
	metajson "github.com/cocoonstack/cocoon/meta/json"
	"github.com/cocoonstack/cocoon/meta/tombstone"
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

// VMsDir is the directory holding every per-VM state directory.
func VMsDir(cmd *cobra.Command) string {
	return filepath.Join(Dir(cmd), "vms")
}

// VMDir is the per-VM state directory under VMsDir.
func VMDir(cmd *cobra.Command, name string) string {
	return filepath.Join(VMsDir(cmd), name)
}

// OpenStore opens the cloudimg store at the resolved state dir, returning the command context with it.
func OpenStore(cmd *cobra.Command) (context.Context, *cloudimg.CloudImg, error) {
	ctx := cliutil.CommandContext(cmd)
	conf := cloudimg.NewConfig(Dir(cmd), 0) // 0 = cloudimg's default pull connections
	metaStore, err := metajson.Open(metajson.Namespace{
		Name:     cloudimg.NamespaceName,
		FilePath: conf.IndexFile(),
		LockPath: conf.IndexLock(),
		Codec: metajson.TableCodec{Specs: []metajson.TableSpec{
			{Key: "images", Table: images.TableRecords},
			{Key: tombstone.TableName, Table: tombstone.TableName, Optional: true},
		}},
	})
	if err != nil {
		return ctx, nil, fmt.Errorf("open meta store: %w", err)
	}
	s, err := cloudimg.New(ctx, Dir(cmd), 0, metaStore)
	if err != nil {
		return ctx, nil, fmt.Errorf("init cloudimg store: %w", err)
	}
	return ctx, s, nil
}
