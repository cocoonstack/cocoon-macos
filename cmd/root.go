package cmd

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/projecteru2/core/log"
	"github.com/projecteru2/core/types"
	"github.com/spf13/cobra"

	"github.com/cocoonstack/cocoon-macos/cmd/image"
	"github.com/cocoonstack/cocoon-macos/cmd/vm"
	"github.com/cocoonstack/cocoon-macos/version"
)

// Execute runs the cocoon-macos CLI.
func Execute() {
	ctx := context.Background()
	logger := log.WithFunc("cmd.Execute")
	if err := setupLog(ctx); err != nil {
		logger.Fatalf(ctx, err, "setup log: %v", err)
	}
	// run() owns the signal context + its deferred cleanup, so os.Exit below
	// never strands a pending defer (gocritic exitAfterDefer).
	if err := run(ctx); err != nil {
		logger.Error(ctx, err, "command failed")
		os.Exit(1)
	}
}

// run executes the root command under a signal-cancellable context.
func run(ctx context.Context) error {
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	root := &cobra.Command{
		Use:           "cocoon-macos",
		Short:         "Run full macOS (Tahoe 26) as a QEMU/KVM guest on x86 Linux",
		Version:       version.String(),
		SilenceUsage:  true,
		SilenceErrors: true, // we log the error ourselves in Execute; don't let cobra double-print it
	}
	root.SetVersionTemplate("{{.Version}}")
	root.AddCommand(vm.Command(vm.NewHandler()))
	root.AddCommand(image.Command(image.NewHandler()))
	return root.ExecuteContext(ctx)
}

// setupLog initializes core/log. SetupLog binds to os.Stdout, so swap in stderr
// for the call to keep command output (e.g. the vm list table) on stdout while
// logs go to stderr.
func setupLog(ctx context.Context) error {
	level := os.Getenv("COCOON_MACOS_LOG_LEVEL")
	if level == "" {
		level = "info"
	}
	origStdout := os.Stdout
	os.Stdout = os.Stderr
	defer func() { os.Stdout = origStdout }()
	return log.SetupLog(ctx, &types.ServerLogConfig{Level: level}, "")
}
