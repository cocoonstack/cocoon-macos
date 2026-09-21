//go:build !linux

package vm

import (
	"github.com/spf13/cobra"

	"github.com/cocoonstack/cocoon/gc"
)

func registerNetGC(*gc.Orchestrator, *cobra.Command) error { return nil }
