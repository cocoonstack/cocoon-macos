package vm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/projecteru2/core/log"
	"github.com/spf13/cobra"

	"github.com/cocoonstack/cocoon-macos/home"
	"github.com/cocoonstack/cocoon-macos/internal/procutil"
	"github.com/cocoonstack/cocoon-macos/qemu"
	"github.com/cocoonstack/cocoon/cmd/cliutil"
	"github.com/cocoonstack/cocoon/utils"
)

func (h *Handler) Create(cmd *cobra.Command, args []string) error {
	return h.createVM(cmd, args[0], false)
}

func (h *Handler) Run(cmd *cobra.Command, args []string) error {
	return h.createVM(cmd, args[0], true)
}

func (h *Handler) Start(cmd *cobra.Command, args []string) error {
	ctx := cliutil.CommandContext(cmd)
	vnc, _ := cmd.Flags().GetInt("vnc")
	vncPass, _ := cmd.Flags().GetString("vnc-password")
	for _, n := range args {
		dir, err := home.VMDir(cmd, n)
		if err != nil {
			return err
		}
		if err := withVMLock(ctx, dir, func() error {
			r, err := loadRec(dir)
			if err != nil {
				return err
			}
			// an op that held the lock (export, a racing run) may have restarted qemu; adopt it and only repair a dead vnc proxy
			running, err := reconcileRunningQEMU(dir, r)
			if err != nil {
				return err
			}
			if running {
				if r.Netns != "" && r.VNCDisp >= 0 && !vncProxyRunning(dir) {
					if err := startVNCProxy(ctx, dir, r.VNCDisp); err != nil {
						return fmt.Errorf("repair vnc proxy: %w", err)
					}
				}
				if cmd.Flags().Changed("vnc") || cmd.Flags().Changed("vnc-password") {
					fmt.Printf("%s (pid %d, already running; supplied VNC settings ignored because live QEMU cannot be retargeted)\n", n, r.PID)
				} else {
					fmt.Printf("%s (pid %d, already running)\n", n, r.PID)
				}
				return nil
			}
			r.VNCDisp, r.VNCPass = vnc, vncPass
			if err := h.launch(cmd, dir, r); err != nil {
				return err
			}
			toggleNet(cmd, r, true)
			fmt.Printf("%s (pid %d)\n", n, r.PID)
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

func (h *Handler) Stop(cmd *cobra.Command, args []string) error {
	grace := graceFromFlags(cmd)
	ctx := cliutil.CommandContext(cmd)
	for _, n := range args {
		dir, err := home.VMDir(cmd, n)
		if err != nil {
			return err
		}
		if err := withVMLock(ctx, dir, func() error {
			r, err := loadRec(dir)
			if err != nil {
				return err
			}
			if _, err := reconcileRunningQEMU(dir, r); err != nil {
				return err
			}
			terminate(ctx, r, grace)
			toggleNet(cmd, r, false)
			stopVNCProxy(ctx, dir)
			r.PID, r.VNCDisp, r.VNCPass, r.VNCPassSet = 0, -1, "", false // VNC is launch-scoped: gone with the qemu it belonged to
			return saveRec(dir, r)
		}); err != nil {
			return err
		}
		fmt.Println(n)
	}
	return nil
}

func (h *Handler) RM(cmd *cobra.Command, args []string) error {
	grace := graceFromFlags(cmd)
	ctx := cliutil.CommandContext(cmd)
	for _, n := range args {
		dir, err := home.VMDir(cmd, n)
		if err != nil {
			return err
		}
		if err := withVMLock(ctx, dir, func() error {
			if _, err := os.Stat(dir); os.IsNotExist(err) {
				return nil
			} else if err != nil {
				return fmt.Errorf("stat vm dir: %w", err)
			}
			// the flock stops a concurrent create/start from changing state between terminate and RemoveAll
			if r, err := loadRec(dir); err == nil {
				if _, err := reconcileRunningQEMU(dir, r); err != nil {
					return err
				}
				terminate(ctx, r, grace)
				stopVNCProxy(ctx, dir)
				if err := teardownNet(ctx, cmd, r); err != nil {
					return err
				}
			} else if cleanupErr := reapStrayHelpers(ctx, dir); cleanupErr != nil {
				return cleanupErr
			}
			if err := os.RemoveAll(dir); err != nil {
				return fmt.Errorf("remove vm dir: %w", err)
			}
			return nil
		}); err != nil {
			return err
		}
		fmt.Println(n)
	}
	return nil
}

func (h *Handler) createVM(cmd *cobra.Command, image string, launch bool) error {
	name := requestedVMName(cmd, "macos-"+time.Now().Format("20060102-150405"))
	dir, err := home.VMDir(cmd, name)
	if err != nil {
		return err
	}
	ctx := cliutil.CommandContext(cmd)
	return withVMLock(ctx, dir, func() error {
		r, err := h.create(cmd, image, name)
		if err != nil {
			return err
		}
		if !launch {
			fmt.Println(r.Name)
			return nil
		}
		if err := h.launch(cmd, dir, r); err != nil {
			return errors.Join(err, cleanupFailedVM(ctx, cmd, dir, r))
		}
		fmt.Printf("%s (pid %d)\n", r.Name, r.PID)
		return nil
	})
}

func (h *Handler) create(cmd *cobra.Command, image, name string) (r *record, retErr error) {
	rawDisks, _ := cmd.Flags().GetStringArray("data-disk")
	diskSpecs, err := parseDataDisks(rawDisks, nil) // fail fast before any scaffolding
	if err != nil {
		return nil, err
	}
	cpus, _ := cmd.Flags().GetInt("cpus")
	if err = validateMacOSCPUs(cpus); err != nil {
		return nil, err
	}
	vnc, _ := cmd.Flags().GetInt("vnc")
	vncPass, _ := cmd.Flags().GetString("vnc-password")
	netMode, _ := cmd.Flags().GetString("net")
	if err = requireCNIVNCPassword(netMode == netCNI, vnc, vncPass); err != nil {
		return nil, err
	}
	storage, err := storageFromFlag(cmd)
	if err != nil {
		return nil, err
	}
	oc, code, varsTmpl, err := resolveFirmware(cmd)
	if err != nil {
		return nil, err
	}
	ctx := cliutil.CommandContext(cmd)
	dir, overlay, ovmfVars, digest, err := scaffoldVM(cmd, name, image, varsTmpl, "OVMF_VARS.fd")
	if err != nil {
		return nil, err
	}
	defer func() {
		if retErr != nil {
			retErr = errors.Join(retErr, cleanupFailedVM(ctx, cmd, dir, r))
		}
	}()
	storage, err = resizeSystemDisk(ctx, overlay, storage)
	if err != nil {
		return nil, err
	}
	mem, _ := cmd.Flags().GetString("memory")
	ssh, _ := cmd.Flags().GetInt("ssh-port")
	tap, _ := cmd.Flags().GetString("tap")
	huge, _ := cmd.Flags().GetBool("hugepages")
	exitOnReboot, _ := cmd.Flags().GetBool("exit-on-reboot")
	cniConfDir, _ := cmd.Flags().GetString("cni-conf-dir")
	cniBinDir, _ := cmd.Flags().GetString("cni-bin-dir")
	r = &record{
		Name: name, Image: image, ImageDigest: digest, Disk: overlay, OVMFCode: code, OVMFVars: ovmfVars,
		CPUs: cpus, Memory: mem, Storage: storage, VNCDisp: vnc, SSHPort: ssh, VNCPass: vncPass, NetMode: netMode, Tap: tap, Hugepages: huge,
		ExitOnReboot: exitOnReboot, CNIConfDir: cniConfDir, CNIBinDir: cniBinDir,
		VMID: utils.GenerateID(), Created: time.Now().Format(time.RFC3339),
	}
	if r.DataDisks, err = createDataDisks(ctx, dir, diskSpecs); err != nil {
		return nil, err
	}
	// OpenCore seeds the default guest MAC from ROM before network provisioning.
	randomSMBIOS, _ := cmd.Flags().GetBool("random-smbios")
	if err = prepareOpenCore(ctx, dir, oc, randomSMBIOS, r); err != nil {
		return nil, err
	}
	if err = applyNet(cmd, r); err != nil {
		return nil, err
	}
	return r, saveRec(dir, r)
}

func (h *Handler) launch(cmd *cobra.Command, dir string, r *record) error {
	ctx := cliutil.CommandContext(cmd)
	logger := log.WithFunc("cmd.vm.launch")
	if err := requireCNIVNCPassword(r.Netns != "", r.VNCDisp, r.VNCPass); err != nil {
		return err
	}
	if hostIsAMD() {
		// macOS reads MSRs an AMD host lacks; without kvm.ignore_msrs KVM injects #GP (best-effort, host-global)
		if err := os.WriteFile("/sys/module/kvm/parameters/ignore_msrs", []byte("1\n"), 0o600); err != nil {
			logger.Warnf(ctx, "set kvm ignore_msrs for AMD: %v", err)
		}
	}
	spec := qemu.Spec{
		Name: r.Name, Disk: r.Disk, OpenCore: r.OpenCore, OVMFCode: r.OVMFCode, OVMFVars: r.OVMFVars,
		CPUs: r.CPUs, Memory: r.Memory, VNCDisp: r.VNCDisp, SSHPort: r.SSHPort, MAC: r.MAC, VNCPass: r.VNCPass,
		Tap:          r.Tap, // set for tap/bridge/cni (a real host TAP); empty => user-mode SLIRP
		Hugepages:    r.Hugepages,
		ExitOnReboot: r.ExitOnReboot,
		DataDisks:    r.DataDisks,
		MonSock:      filepath.Join(dir, "monitor.sock"), QMPSock: filepath.Join(dir, "qmp.sock"),
	}
	// CNI: a 127.0.0.1 VNC inside the netns is unreachable; use a unix socket fronted by startVNCProxy
	if r.Netns != "" && r.VNCDisp >= 0 {
		spec.VNCSock = filepath.Join(dir, vncSockName)
	}
	pidfile := filepath.Join(dir, "qemu.pid")
	args := append(spec.Args(), "-daemonize", "-pidfile", pidfile)
	stopVNCProxy(ctx, dir)
	ensureNetnsLoopback(ctx, r)
	if r.Netns != "" {
		logger.Debugf(ctx, "running qemu in netns %s via `ip netns exec`", filepath.Base(r.Netns))
	}
	logger.Debug(ctx, "booting macOS guest via qemu-system-x86_64 (authoritative VMM; no Go equivalent)")
	r.VNCPassSet = r.VNCPass != ""
	if err := saveRec(dir, r); err != nil {
		return err
	}
	c := launchCmd(r, args)
	c.Stdout, c.Stderr = os.Stdout, os.Stderr
	if err := c.Run(); err != nil {
		return fmt.Errorf("launch qemu: %w", err)
	}
	pid, err := utils.ReadPIDFile(pidfile)
	if err != nil {
		return fmt.Errorf("read qemu pid file: %w", err)
	}
	r.PID = pid
	if !isRunning(r) {
		return fmt.Errorf("qemu pid %d is not running for disk %s", r.PID, r.Disk)
	}
	if r.VNCPass != "" {
		if err := setVNCPassword(ctx, spec.MonSock, r.VNCPass); err != nil {
			// qemu keeps password=on with no password set, so every VNC auth would fail
			terminate(ctx, r, 0)
			return fmt.Errorf("set vnc password: %w", err)
		}
	}
	if spec.VNCSock != "" {
		if err := startVNCProxy(ctx, dir, r.VNCDisp); err != nil {
			terminate(ctx, r, 0)
			return fmt.Errorf("start vnc proxy: %w", err)
		}
	}
	return saveRec(dir, r)
}

func requestedVMName(cmd *cobra.Command, fallback string) string {
	name, _ := cmd.Flags().GetString("name")
	if name != "" {
		return name
	}
	return fallback
}

func validateMacOSCPUs(cpus int) error {
	if cpus < 1 || cpus%2 != 0 {
		return fmt.Errorf("--cpus must be a positive even number, got %d", cpus)
	}
	return nil
}

// cleanupFailedVM uses an uncanceled bounded context so cancellation still reaps helpers, networking and QEMU.
func cleanupFailedVM(ctx context.Context, cmd *cobra.Command, dir string, r *record) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), vmCleanupTimeout)
	defer cancel()
	var errs []error
	if r != nil {
		terminate(ctx, r, 0)
		stopVNCProxy(ctx, dir)
		if err := teardownNet(ctx, cmd, r); err != nil {
			errs = append(errs, err)
		}
	}
	if err := reapStrayHelpers(ctx, dir); err != nil {
		errs = append(errs, err)
	}
	if err := os.RemoveAll(dir); err != nil {
		errs = append(errs, fmt.Errorf("remove failed vm dir: %w", err))
	}
	return errors.Join(errs...)
}

func reapStrayHelpers(ctx context.Context, dir string) error {
	return errors.Join(
		procutil.TerminateByCmdline(ctx, qemuBinary, vmDirPrefix(dir), 0),
		procutil.TerminateByCmdline(ctx, "qemu-nbd", vmDirPrefix(dir), time.Second),
	)
}

// prepareOpenCore points r.OpenCore at the shared base, or with randomSMBIOS at a per-VM overlay whose config.plist is patched with a unique identity.
func prepareOpenCore(ctx context.Context, dir, ocBase string, randomSMBIOS bool, r *record) error {
	if !randomSMBIOS {
		r.OpenCore, r.OpenCoreBase = ocBase, ""
		return nil
	}
	sm, err := qemu.RandomSMBIOS()
	if err != nil {
		return err
	}
	ocOverlay := filepath.Join(dir, "OpenCore.qcow2")
	if err := bakeOverlay(ctx, ocBase, ocOverlay); err != nil {
		return err
	}
	log.WithFunc("cmd.vm.prepareOpenCore").Debugf(ctx, "patching OpenCore %s via qemu-nbd (smbios)", ocOverlay)
	if err := qemu.InjectConfig(ctx, ocOverlay, &sm); err != nil {
		return fmt.Errorf("inject opencore config: %w", err)
	}
	r.OpenCore, r.OpenCoreBase = ocOverlay, ocBase
	r.SMBIOS, r.MAC = &sm, sm.MAC()
	return nil
}
