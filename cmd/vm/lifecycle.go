package vm

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/projecteru2/core/log"
	"github.com/spf13/cobra"

	"github.com/cocoonstack/cocoon-macos/home"
	"github.com/cocoonstack/cocoon-macos/internal/procutil"
	"github.com/cocoonstack/cocoon-macos/qemu"
	"github.com/cocoonstack/cocoon/cmd/cliutil"
	"github.com/cocoonstack/cocoon/utils"
)

func Create(cmd *cobra.Command, args []string) error {
	return createVM(cmd, args[0], false)
}

func Run(cmd *cobra.Command, args []string) error {
	return createVM(cmd, args[0], true)
}

func Start(cmd *cobra.Command, args []string) error {
	vnc, _ := cmd.Flags().GetInt("vnc")
	vncPass, _ := cmd.Flags().GetString("vnc-password")
	return forEachVMDir(cmd, args, func(ctx context.Context, n, dir string) error {
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
			ignored := ""
			if cmd.Flags().Changed("vnc") || cmd.Flags().Changed("vnc-password") {
				ignored = "; supplied VNC settings ignored because live QEMU cannot be retargeted"
			}
			fmt.Printf("%s (pid %d, already running%s)\n", n, r.PID, ignored)
			return nil
		}
		r.VNCDisp, r.VNCPass = vnc, vncPass
		if err := launch(cmd, dir, r); err != nil {
			return err
		}
		toggleNet(cmd, r, true)
		fmt.Printf("%s (pid %d)\n", n, r.PID)
		return nil
	})
}

func Stop(cmd *cobra.Command, args []string) error {
	grace := graceFromFlags(cmd)
	return forEachVMDir(cmd, args, func(ctx context.Context, n, dir string) error {
		r, err := loadRec(dir)
		if err != nil {
			return err
		}
		if _, err := reconcileRunningQEMU(dir, r); err != nil {
			return err
		}
		if err := stopInstance(ctx, dir, r, grace); err != nil {
			return err
		}
		toggleNet(cmd, r, false)
		r.PID, r.VNCDisp, r.VNCPass, r.VNCPassSet = 0, -1, "", false // VNC is launch-scoped: gone with the qemu it belonged to
		if err := saveRec(dir, r); err != nil {
			return err
		}
		fmt.Println(n)
		return nil
	})
}

func RM(cmd *cobra.Command, args []string) error {
	grace := graceFromFlags(cmd)
	return forEachVMDir(cmd, args, func(ctx context.Context, n, dir string) error {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			fmt.Println(n)
			return nil
		} else if err != nil {
			return fmt.Errorf("stat vm dir: %w", err)
		}
		// the flock stops a concurrent create/start from changing state between terminate and RemoveAll
		if r, err := loadRec(dir); err == nil {
			if _, err := reconcileRunningQEMU(dir, r); err != nil {
				return err
			}
			if err := stopInstance(ctx, dir, r, grace); err != nil {
				return err
			}
			if err := teardownNet(ctx, cmd, r); err != nil {
				return err
			}
		} else if cleanupErr := reapStrayHelpers(ctx, dir); cleanupErr != nil {
			return cleanupErr
		}
		if err := os.RemoveAll(dir); err != nil {
			return fmt.Errorf("remove vm dir: %w", err)
		}
		fmt.Println(n)
		return nil
	})
}

func createVM(cmd *cobra.Command, image string, start bool) error {
	name := requestedVMName(cmd, "macos-"+time.Now().Format("20060102-150405"))
	dir, err := home.VMDir(cmd, name)
	if err != nil {
		return err
	}
	ctx := cliutil.CommandContext(cmd)
	return withVMLock(ctx, dir, func() error {
		r, err := create(cmd, image, name)
		if err != nil {
			return err
		}
		if !start {
			fmt.Println(r.Name)
			return nil
		}
		if err := launch(cmd, dir, r); err != nil {
			return errors.Join(err, cleanupFailedVM(ctx, cmd, dir, r))
		}
		fmt.Printf("%s (pid %d)\n", r.Name, r.PID)
		return nil
	})
}

func create(cmd *cobra.Command, image, name string) (r *record, retErr error) {
	rawDisks, _ := cmd.Flags().GetStringArray("data-disk")
	diskSpecs, err := parseDataDisks(rawDisks, nil) // fail fast before any scaffolding
	if err != nil {
		return nil, err
	}
	cpus, _ := cmd.Flags().GetInt("cpus")
	if err = validateMacOSCPUs(cpus); err != nil {
		return nil, err
	}
	mem, _ := cmd.Flags().GetString("memory")
	if err = validateMemory(mem); err != nil {
		return nil, err
	}
	vnc, _ := cmd.Flags().GetInt("vnc")
	vncPass, _ := cmd.Flags().GetString("vnc-password")
	netMode, _ := cmd.Flags().GetString("net")
	if err = requireCNIVNCPassword(netMode == netCNI, vnc, vncPass); err != nil {
		return nil, err
	}
	tap, _ := cmd.Flags().GetString("tap")
	if err = validateTapFlag(netMode, tap); err != nil {
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
	dir, overlay, ovmfVars, digest, err := scaffoldVM(cmd, name, image, varsTmpl)
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
	ssh, _ := cmd.Flags().GetInt("ssh-port")
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

func launch(cmd *cobra.Command, dir string, r *record) error {
	ctx := cliutil.CommandContext(cmd)
	logger := log.WithFunc("cmd.vm.launch")
	if err := requireCNIVNCPassword(r.Netns != "", r.VNCDisp, r.VNCPass); err != nil {
		return err
	}
	if isHostAMD() {
		// macOS reads MSRs an AMD host lacks; without kvm.ignore_msrs KVM injects #GP (best-effort, host-global)
		if err := os.WriteFile("/sys/module/kvm/parameters/ignore_msrs", []byte("1\n"), 0o600); err != nil {
			logger.Warnf(ctx, "set kvm ignore_msrs for AMD: %v", err)
		}
	}
	spec := qemu.Spec{
		Disk: r.Disk, OpenCore: r.OpenCore, OVMFCode: r.OVMFCode, OVMFVars: r.OVMFVars,
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
	pidfile := qemuPIDPath(r.Disk)
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
			return errors.Join(fmt.Errorf("set vnc password: %w", err), terminate(ctx, r, 0))
		}
	}
	if spec.VNCSock != "" {
		if err := startVNCProxy(ctx, dir, r.VNCDisp); err != nil {
			return errors.Join(fmt.Errorf("start vnc proxy: %w", err), terminate(ctx, r, 0))
		}
	}
	return saveRec(dir, r)
}

func requestedVMName(cmd *cobra.Command, fallback string) string {
	name, _ := cmd.Flags().GetString("name")
	return cmp.Or(name, fallback)
}

func validateMacOSCPUs(cpus int) error {
	if cpus < 1 || cpus%2 != 0 {
		return fmt.Errorf("--cpus must be a positive even number, got %d", cpus)
	}
	return nil
}

func validateMemory(mem string) error {
	if n, err := strconv.Atoi(mem); err != nil || n <= 0 {
		return fmt.Errorf("--memory must be a positive MiB count, got %q", mem)
	}
	return nil
}

func cleanupFailedVM(ctx context.Context, cmd *cobra.Command, dir string, r *record) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), vmCleanupTimeout)
	defer cancel()
	var errs []error
	if r != nil {
		errs = append(errs, stopInstance(ctx, dir, r, 0), teardownNet(ctx, cmd, r))
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
