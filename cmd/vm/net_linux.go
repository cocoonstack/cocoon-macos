//go:build linux

package vm

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"

	"github.com/projecteru2/core/log"
	"github.com/spf13/cobra"

	"github.com/cocoonstack/cocoon/config"
	"github.com/cocoonstack/cocoon/network"
	"github.com/cocoonstack/cocoon/network/bridge"
	"github.com/cocoonstack/cocoon/network/cni"
	"github.com/cocoonstack/cocoon/types"

	"github.com/cocoonstack/cocoon-macos/internal/home"
)

// --net modes available only on Linux (cocoon's bridge/CNI network plane).
const (
	netBridge = "bridge"
	netCNI    = "cni"
)

// newProvider builds the cocoon host-side network provider for r.NetMode. "tap"/"bridge" both
// use the bridge backend (QEMU -netdev tap,ifname= opens the device in the host netns, so the
// TAP must be a host-side bridge port); "cni" uses the CNI backend (TAP lives inside a netns).
func newProvider(cmd *cobra.Command, r *record) (network.Network, error) {
	conf := &config.Config{
		RootDir:    home.Dir(cmd),
		DNS:        "8.8.8.8,1.1.1.1",
		CNIConfDir: flagOr(cmd, "cni-conf-dir", "/etc/cni/net.d"),
		CNIBinDir:  flagOr(cmd, "cni-bin-dir", "/opt/cni/bin"),
	}
	switch r.NetMode {
	case netCNI:
		return cni.New(conf)
	case netTAP, netBridge:
		if r.BridgeDev == "" { // persisted at create; flag is only present on create/run/clone, not rm
			r.BridgeDev, _ = cmd.Flags().GetString("bridge")
		}
		if r.BridgeDev == "" {
			return nil, fmt.Errorf("--net %s requires --bridge <dev> (an existing Linux bridge)", r.NetMode)
		}
		return bridge.New(conf, r.BridgeDev)
	}
	return nil, fmt.Errorf("unknown --net mode %q (want user|tap|cni|bridge)", r.NetMode)
}

// prepareNet provisions host-side networking and returns the TAP ifname, the netns path (CNI;
// "" otherwise) and the guest MAC. user-mode and a pre-created --tap need no provisioning; every
// other mode auto-creates a TAP via cocoon (the SAME forwarding plane as cocoon's CH/FC VMs).
func prepareNet(cmd *cobra.Command, r *record) (tap, netns, mac string, err error) {
	switch r.NetMode {
	case "", netUser:
		return "", "", r.MAC, nil
	case netTAP:
		if r.Tap != "" { // user pre-created the TAP (already on a bridge / cocoon CNI) — use verbatim
			return r.Tap, "", r.MAC, nil
		}
	}
	provider, err := newProvider(cmd, r)
	if err != nil {
		return "", "", "", err
	}
	ctx := ctxOf(cmd)
	// CPU=1 => NetNumQueues yields a single-queue TAP matching QEMU's single-queue -netdev tap,ifname=
	vmCfg := &types.VMConfig{Config: types.Config{CPU: 1}, Name: r.Name}
	nsPath, err := provider.Prepare(ctx, r.VMID, vmCfg)
	if err != nil {
		return "", "", "", fmt.Errorf("prepare network: %w", err)
	}
	cfgs, err := provider.Add(ctx, r.VMID, vmCfg, network.AddRange(0, 1)...)
	if err != nil {
		return "", "", "", fmt.Errorf("add network: %w", err)
	}
	if len(cfgs) == 0 {
		return "", "", "", errors.New("network add returned no NIC")
	}
	mac = r.MAC // SMBIOS ROM wins as the guest MAC; cocoon's generated MAC is only a fallback
	if mac == "" {
		mac = cfgs[0].MAC
	}
	return cfgs[0].TAP, nsPath, mac, nil
}

// teardownNet removes an auto-created TAP/netns. Best-effort; never touches a user-supplied --tap.
func teardownNet(cmd *cobra.Command, r *record) {
	if !r.TapOwned {
		return
	}
	if provider, err := newProvider(cmd, r); err == nil {
		_, _ = provider.Delete(ctxOf(cmd), []string{r.VMID})
	}
	// CleanupTAPs runs unconditionally: it removes bt<vmid>-* by name and must not be gated on
	// newProvider succeeding (rm has no --bridge flag), or an auto-created TAP would leak.
	if r.NetMode == netTAP || r.NetMode == netBridge {
		bridge.CleanupTAPs([]string{r.VMID})
	}
}

// ensureNetnsLoopback brings up lo inside the CNI netns. A freshly-created netns has its loopback
// DOWN, so qemu's -vnc 127.0.0.1:N (and any other loopback bind) fails with EADDRNOTAVAIL until lo
// is up. No-op outside CNI (no netns). Shells out to `ip` because the qemu launch already runs via
// `ip netns exec` (the fork-safe, cgo-free way to run a daemonized process in a netns).
func ensureNetnsLoopback(ctx context.Context, r *record) {
	if r.Netns == "" {
		return
	}
	ns := filepath.Base(r.Netns)
	log.WithFunc("cmd.vm.ensureNetnsLoopback").Debugf(ctx, "bringing up lo in netns %s via `ip netns exec`", ns)
	_ = exec.Command("ip", "netns", "exec", ns, "ip", "link", "set", "lo", "up").Run()
}

// launchCmd builds the qemu exec. For CNI the TAP lives inside a netns, so the qemu process must
// run there (ip netns exec) for -netdev tap,ifname= to find it; ip netns exec is the fork-safe
// path for a daemonized launch (no cgo/setns), which is why this shells out rather than using
// netlink. qemu-system-x86_64 itself is the authoritative VMM with no Go-native equivalent.
func launchCmd(r *record, args []string) *exec.Cmd {
	if r.Netns != "" {
		ns := filepath.Base(r.Netns)
		return exec.Command("ip", append([]string{"netns", "exec", ns, qemuBinary}, args...)...)
	}
	return exec.Command(qemuBinary, args...)
}

func flagOr(cmd *cobra.Command, name, def string) string {
	if v, _ := cmd.Flags().GetString(name); v != "" {
		return v
	}
	return def
}
