//go:build !linux

package vm

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"

	"github.com/spf13/cobra"
)

// provisionNet on non-Linux always fails: auto-create / CNI / bridge need CAP_NET_ADMIN +
// netlink + netns, so they are Linux-only.
func provisionNet(_ *cobra.Command, _ *record) (tap, netns, mac string, err error) {
	return "", "", "", fmt.Errorf("auto-create TAP / CNI / bridge networking requires Linux (running on %s); pass a pre-created --tap or use --net user", runtime.GOOS)
}

func teardownNet(_ *cobra.Command, _ *record) {}

// launchCmd builds the qemu exec; qemu-system-x86_64 is the authoritative VMM with no Go-native
// equivalent (and off Linux there is no netns to enter).
func launchCmd(_ *record, args []string) *exec.Cmd {
	return exec.Command(qemuBinary, args...)
}

// ensureNetnsLoopback is a no-op off Linux (no CNI/netns there).
func ensureNetnsLoopback(_ context.Context, _ *record) {}
