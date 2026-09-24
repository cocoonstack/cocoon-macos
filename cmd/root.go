// Package cmd wires cocoon-macos subcommands.
package cmd

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/projecteru2/core/log"
	"github.com/projecteru2/core/types"
	"github.com/spf13/cobra"

	"github.com/cocoonstack/cocoon-macos/cmd/image"
	"github.com/cocoonstack/cocoon-macos/cmd/vm"
	"github.com/cocoonstack/cocoon-macos/home"
	"github.com/cocoonstack/cocoon-macos/version"
)

// Execute runs the cocoon-macos CLI.
func Execute() {
	ctx := context.Background()
	logger := log.WithFunc("cmd.Execute")
	if err := setupLog(ctx); err != nil {
		logger.Fatalf(ctx, err, "setup log")
	}
	// run() owns the signal context + its deferred cleanup, so os.Exit never strands a pending defer
	if err := run(ctx); err != nil {
		logger.Error(ctx, err, "command failed")
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	root := &cobra.Command{
		Use:               "cocoon-macos",
		Short:             "Run full macOS (Tahoe 26) as a QEMU/KVM guest on x86 Linux",
		Version:           version.String(),
		SilenceUsage:      true,
		SilenceErrors:     true, // Execute logs the error itself; don't let cobra double-print it
		PersistentPreRunE: resolvePaths,
	}
	root.SetVersionTemplate("{{.Version}}")
	root.PersistentFlags().String("state-dir", "", "state root (default $COCOON_MACOS_HOME or "+home.Default+")")
	root.AddCommand(vm.Command())
	root.AddCommand(vm.GCCommand())
	root.AddCommand(image.Command())
	return root.ExecuteContext(ctx)
}

func resolvePaths(cmd *cobra.Command, _ []string) error {
	for _, name := range []string{"state-dir", "cni-conf-dir", "cni-bin-dir"} {
		path, _ := cmd.Flags().GetString(name)
		if name == "state-dir" {
			path = home.Dir(cmd)
		} else if path == "" {
			continue
		}
		path, err := filepath.Abs(path)
		if err != nil {
			return fmt.Errorf("resolve --%s: %w", name, err)
		}
		if err := cmd.Flags().Set(name, path); err != nil {
			return err
		}
	}
	return nil
}

// setupLog swaps stderr in because SetupLog binds whatever os.Stdout is at call time.
func setupLog(ctx context.Context) error {
	level := cmp.Or(os.Getenv("COCOON_MACOS_LOG_LEVEL"), "info")
	origStdout := os.Stdout
	os.Stdout = os.Stderr
	defer func() { os.Stdout = origStdout }()
	return log.SetupLog(ctx, &types.ServerLogConfig{Level: level, UseJSON: !stderrIsTerminal()}, "")
}

func stderrIsTerminal() bool {
	fi, err := os.Stderr.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
