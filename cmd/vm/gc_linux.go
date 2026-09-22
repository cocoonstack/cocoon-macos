//go:build linux

package vm

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/cocoonstack/cocoon/gc"
	metajson "github.com/cocoonstack/cocoon/meta/json"
	"github.com/cocoonstack/cocoon/network"
	"github.com/cocoonstack/cocoon/network/bridge"
	"github.com/cocoonstack/cocoon/network/cni"
)

// registerNetGC adds cocoon's cni and bridge collectors under the cm scope, so only cocoon-macos's own netns and TAPs are candidates.
func registerNetGC(o *gc.Orchestrator, cmd *cobra.Command, inUse network.VMInUse) error {
	conf := netConf(cmd, &record{})
	store, err := metajson.Open(cni.NewConfig(conf).JSONNamespace())
	if err != nil {
		return fmt.Errorf("open meta store: %w", err)
	}
	c, err := cni.New(conf, store)
	if err != nil {
		return err
	}
	gc.Register(o, c.GCModule(inUse))
	gc.Register(o, bridge.GCModule(conf.BridgeTAPPrefix(), inUse))
	return nil
}
