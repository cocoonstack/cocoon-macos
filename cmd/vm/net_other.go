//go:build !linux

package vm

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"

	"github.com/spf13/cobra"
)

// prepareNet on non-Linux supports only user-mode and a pre-created --tap; auto-create / CNI /
// bridge need CAP_NET_ADMIN + netlink + netns, so they are Linux-only.
func prepareNet(_ *cobra.Command, r *record) (tap, netns, mac string, err error) {
	switch r.NetMode {
	case "", netUser:
		return "", "", r.MAC, nil
	case netTAP:
		if r.Tap != "" {
			return r.Tap, "", r.MAC, nil
		}
	}
	return "", "", "", fmt.Errorf("auto-create TAP / CNI / bridge networking requires Linux (running on %s); pass a pre-created --tap or use --net user", runtime.GOOS)
}

func teardownNet(_ *cobra.Command, _ *record) {}

func launchCmd(_ *record, args []string) *exec.Cmd {
	return exec.Command(qemuBinary, args...)
}

// ensureNetnsLoopback is a no-op off Linux (no CNI/netns there).
func ensureNetnsLoopback(_ context.Context, _ *record) {}
