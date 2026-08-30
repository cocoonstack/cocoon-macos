//go:build !linux

package vm

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"

	"github.com/spf13/cobra"
)

func provisionNet(_ *cobra.Command, _ *record) (tap, netns, mac string, err error) {
	return "", "", "", fmt.Errorf("auto-create TAP / CNI / bridge networking requires Linux (running on %s); pass a pre-created --tap or use --net user", runtime.GOOS)
}

func teardownNet(_ context.Context, _ *cobra.Command, _ *record) error { return nil }

func quiesceNet(_ *cobra.Command, _ *record) {}

func unquiesceNet(_ *cobra.Command, _ *record) {}

func launchCmd(_ *record, args []string) *exec.Cmd {
	return exec.Command(qemuBinary, args...)
}

func ensureNetnsLoopback(_ context.Context, _ *record) {}
