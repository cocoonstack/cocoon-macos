//go:build linux

package vm

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"

	"github.com/projecteru2/core/log"
	"github.com/spf13/cobra"
	"github.com/vishvananda/netlink"

	"github.com/cocoonstack/cocoon-macos/home"
	"github.com/cocoonstack/cocoon/cmd/cliutil"
	"github.com/cocoonstack/cocoon/config"
	metajson "github.com/cocoonstack/cocoon/meta/json"
	"github.com/cocoonstack/cocoon/network"
	"github.com/cocoonstack/cocoon/network/bridge"
	"github.com/cocoonstack/cocoon/network/cni"
	"github.com/cocoonstack/cocoon/types"
)

// netScope keys cocoon-macos's host TAP/netns families apart from a co-hosted cocoon's, so neither GC reclaims the other's live guests.
const netScope = "cm"

// netConf is the cocoon network config: bridge/CNI provisioning shares cocoon's forwarding plane, keyed under our own device family; the CNI dirs resolve flag, then record, then default.
func netConf(cmd *cobra.Command, r *record) *config.Config {
	return &config.Config{
		RootDir:    home.Dir(cmd),
		DNS:        "8.8.8.8,1.1.1.1",
		CNIConfDir: cmp.Or(flagOr(cmd, "cni-conf-dir", ""), r.CNIConfDir, "/etc/cni/net.d"),
		CNIBinDir:  cmp.Or(flagOr(cmd, "cni-bin-dir", ""), r.CNIBinDir, "/opt/cni/bin"),
		NetScope:   netScope,
	}
}

// newProvider builds the cocoon network provider: tap/bridge both use the bridge backend (QEMU opens the TAP in the host netns, so it must be a host-side bridge port); cni's TAP lives in a netns.
func newProvider(cmd *cobra.Command, r *record) (network.Network, error) {
	conf := netConf(cmd, r)
	switch r.NetMode {
	case netCNI:
		store, err := metajson.Open(cni.NewConfig(conf).JSONNamespace())
		if err != nil {
			return nil, fmt.Errorf("open meta store: %w", err)
		}
		return cni.New(conf, store)
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

// provisionNet auto-creates a TAP via cocoon — the SAME forwarding plane as cocoon's CH/FC VMs.
func provisionNet(cmd *cobra.Command, r *record) (tap, netns, mac string, err error) {
	provider, err := newProvider(cmd, r)
	if err != nil {
		return "", "", "", err
	}
	ctx := cliutil.CommandContext(cmd)
	// CPU=1 => NetNumQueues yields a single-queue TAP matching QEMU's single-queue -netdev tap,ifname=
	vmCfg := &types.VMConfig{CPU: 1, Name: r.Name}
	nsPath, err := provider.Prepare(ctx, r.VMID, vmCfg)
	if err != nil {
		return "", "", "", fmt.Errorf("prepare network: %w", err)
	}
	cfgs, err := provider.Add(ctx, r.VMID, vmCfg, network.AddRange(0, 1)...)
	if err == nil && len(cfgs) == 0 {
		err = errors.New("network add returned no NIC")
	}
	if err != nil {
		// Add rolls back only what it created itself; the netns from Prepare is ours to drop
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), vmCleanupTimeout)
		defer cancel()
		if delErr := provider.Delete(rctx, r.VMID); delErr != nil {
			log.WithFunc("cmd.vm.provisionNet").Warnf(rctx, "rollback network for %s: %v", r.VMID, delErr)
		}
		return "", "", "", fmt.Errorf("add network: %w", err)
	}
	mac = cfgs[0].MAC
	if r.NetMode != netCNI {
		mac = cmp.Or(r.MAC, mac)
	}
	return cfgs[0].TAP, nsPath, mac, nil
}

// teardownNet removes an auto-created TAP/netns and never touches a user-supplied --tap.
func teardownNet(ctx context.Context, cmd *cobra.Command, r *record) error {
	if !r.TapOwned {
		return nil
	}
	bridgeMode := r.NetMode == netTAP || r.NetMode == netBridge
	if bridgeMode {
		defer bridge.CleanupTAPs(netConf(cmd, r).BridgeTAPPrefix(), []string{r.VMID})
	}
	logger := log.WithFunc("cmd.vm.teardownNet")
	provider, err := newProvider(cmd, r)
	if err != nil {
		if bridgeMode {
			return nil
		}
		return fmt.Errorf("teardown network for %s: %w", r.VMID, err)
	}
	// quiesce first: covers the idle-TAP softirq gap between the VMM dying and Delete dropping the redirect
	if err := provider.Quiesce(ctx, r.VMID); err != nil {
		logger.Warnf(ctx, "quiesce network for %s: %v", r.VMID, err)
	}
	if err := provider.Delete(ctx, r.VMID); err != nil {
		return fmt.Errorf("teardown network for %s: %w", r.VMID, err)
	}
	return nil
}

// toggleNet downs a stopped VM's owned NICs so a dead VMM's carrier-less TAP can't storm host softirqs via the tc mirred redirect, and brings them back up on start.
func toggleNet(cmd *cobra.Command, r *record, up bool) {
	if !r.TapOwned {
		return
	}
	ctx := cliutil.CommandContext(cmd)
	logger := log.WithFunc("cmd.vm.toggleNet")
	verb, toggle := "quiesce", network.Network.Quiesce
	if up {
		verb, toggle = "unquiesce", network.Network.Unquiesce
	}
	if provider, err := newProvider(cmd, r); err != nil {
		logger.Warnf(ctx, "%s network for %s: %v", verb, r.VMID, err)
	} else if err := toggle(provider, ctx, r.VMID); err != nil {
		logger.Warnf(ctx, "%s network for %s: %v", verb, r.VMID, err)
	}
	setTapLink(ctx, r, up)
}

// setTapLink flips a host-netns TAP's admin state: cocoon's bridge backend no-ops Quiesce, so the toggle lives here; a CNI TAP is inside a netns and is the provider's job.
func setTapLink(ctx context.Context, r *record, up bool) {
	if r.Tap == "" || r.Netns != "" {
		return
	}
	logger := log.WithFunc("cmd.vm.setTapLink")
	link, err := netlink.LinkByName(r.Tap)
	if err != nil {
		logger.Warnf(ctx, "find tap %s: %v", r.Tap, err)
		return
	}
	set := netlink.LinkSetUp
	if !up {
		set = netlink.LinkSetDown
	}
	if err := set(link); err != nil {
		logger.Warnf(ctx, "set tap %s up=%v: %v", r.Tap, up, err)
	}
}

// ensureNetnsLoopback brings up lo inside the CNI netns — a fresh netns has it DOWN, so qemu's 127.0.0.1 binds would fail with EADDRNOTAVAIL.
func ensureNetnsLoopback(ctx context.Context, r *record) {
	if r.Netns == "" {
		return
	}
	ns := filepath.Base(r.Netns)
	log.WithFunc("cmd.vm.ensureNetnsLoopback").Debugf(ctx, "bringing up lo in netns %s via `ip netns exec`", ns)
	_ = exec.Command("ip", "netns", "exec", ns, "ip", "link", "set", "lo", "up").Run()
}

// launchCmd builds the qemu exec, wrapped in `ip netns exec` for CNI so -netdev tap finds the in-netns TAP (the fork-safe, cgo-free way to daemonize into a netns).
func launchCmd(r *record, args []string) *exec.Cmd {
	if r.Netns != "" {
		ns := filepath.Base(r.Netns)
		return exec.Command("ip", append([]string{"netns", "exec", ns, qemuBinary}, args...)...)
	}
	return exec.Command(qemuBinary, args...)
}
