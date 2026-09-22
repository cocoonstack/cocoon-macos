//go:build !linux

package vm

import (
	"github.com/spf13/cobra"

	"github.com/cocoonstack/cocoon/gc"
	"github.com/cocoonstack/cocoon/network"
)

func registerNetGC(*gc.Orchestrator, *cobra.Command, network.VMInUse) error { return nil }
