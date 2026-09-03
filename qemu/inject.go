package qemu

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/projecteru2/core/log"
	"howett.net/plist"

	"github.com/cocoonstack/cocoon-macos/internal/procutil"
	"github.com/cocoonstack/cocoon/lock/flock"
	"github.com/cocoonstack/cocoon/utils"
)

const (
	nbdDeviceCount    = 16
	nbdLeasePath      = "/run/lock/cocoon-macos-nbd.lock"
	nbdCleanupTimeout = 15 * time.Second
	nbdCommandTimeout = 5 * time.Second
)

type nbdConnection struct {
	device  string
	pid     int
	pidFile string
	ocPath  string
}

// detached from the caller's cancellation: disconnect must still run when the caller is unwinding after a timeout.
func (c *nbdConnection) disconnect(ctx context.Context) error {
	logger := log.WithFunc("qemu.nbdConnection.disconnect")
	ctx = context.WithoutCancel(ctx)
	var commandErr error
	disconnectCtx, disconnectCancel := context.WithTimeout(ctx, nbdCommandTimeout)
	if out, err := exec.CommandContext(disconnectCtx, "qemu-nbd", "--disconnect", c.device).CombinedOutput(); err != nil {
		logger.Warnf(disconnectCtx, "disconnect %s (output: %s): %v", c.device, out, err)
		commandErr = fmt.Errorf("disconnect %s (output: %s): %w", c.device, out, err)
	}
	disconnectCancel()
	waitCtx, waitCancel := context.WithTimeout(ctx, nbdCleanupTimeout)
	defer waitCancel()
	if err := utils.WaitFor(waitCtx, 10*time.Second, 100*time.Millisecond, func() (bool, error) {
		held, scanErr := isFileHeld(c.ocPath)
		return !held, scanErr
	}); err != nil {
		logger.Warnf(waitCtx, "qcow2 %s still held after nbd disconnect: %v", c.ocPath, err)
		killCtx, killCancel := context.WithTimeout(ctx, nbdCleanupTimeout)
		defer killCancel()
		var cleanupErrs []error
		if cleanupErr := utils.TerminateProcess(killCtx, c.pid, "qemu-nbd", c.ocPath, time.Second); cleanupErr != nil {
			logger.Warnf(killCtx, "terminate qemu-nbd pid %d for %s: %v", c.pid, c.ocPath, cleanupErr)
			cleanupErrs = append(cleanupErrs, cleanupErr)
		}
		if cleanupErr := cleanupNBDForPath(killCtx, c.ocPath); cleanupErr != nil {
			logger.Warnf(killCtx, "terminate stale qemu-nbd for %s: %v", c.ocPath, cleanupErr)
			cleanupErrs = append(cleanupErrs, cleanupErr)
		}
		commandErr = errors.Join(commandErr, err, errors.Join(cleanupErrs...))
	}
	_ = os.Remove(c.pidFile)
	held, err := isFileHeld(c.ocPath)
	if err != nil {
		return errors.Join(commandErr, err)
	}
	if !held {
		return nil
	}
	return commandErr
}

// InjectConfig mounts the OpenCore qcow2 via qemu-nbd (the only way to edit a FAT partition inside a qcow2; needs root + the nbd module) and patches config.plist with a per-VM identity.
func InjectConfig(ctx context.Context, ocPath string, sm *SMBIOS) error {
	return withNBDLease(ctx, nbdLeasePath, func() (retErr error) {
		_ = exec.CommandContext(ctx, "modprobe", "nbd", "max_part=8").Run()
		conn, connectErr := connectFreeNBD(ctx, ocPath)
		if connectErr != nil {
			return connectErr
		}
		defer func() { retErr = errors.Join(retErr, conn.disconnect(ctx)) }()
		if waitErr := waitForPart(ctx, conn.device); waitErr != nil {
			return waitErr
		}
		mnt, err := os.MkdirTemp("", "oc-efi-")
		if err != nil {
			return fmt.Errorf("create mount dir: %w", err)
		}
		mounted := false
		defer func() { retErr = errors.Join(retErr, cleanupNBDMount(ctx, mnt, mounted)) }()
		var mountErr error
		for _, p := range []string{conn.device + "p1", conn.device + "p2", conn.device} {
			if mountErr = exec.CommandContext(ctx, "mount", p, mnt).Run(); mountErr == nil {
				mounted = true
				break
			}
		}
		if mountErr != nil {
			return fmt.Errorf("mount OpenCore EFI partition on %s: %w", conn.device, mountErr)
		}
		return patchPlist(filepath.Join(mnt, "EFI", "OC", "config.plist"), sm)
	})
}

// serialize the whole transaction: a free-device check then qemu-nbd --connect is not atomic across processes.
func withNBDLease(ctx context.Context, path string, fn func() error) error {
	l := flock.New(path)
	if err := l.Lock(ctx); err != nil {
		return fmt.Errorf("lock qemu-nbd lease: %w", err)
	}
	defer func() { _ = l.Unlock(context.WithoutCancel(ctx)) }()
	return fn()
}

// waitForPart blocks until the kernel's async partition scan exposes nbdXp1 for the mount.
func waitForPart(ctx context.Context, nbd string) error {
	if err := utils.WaitFor(ctx, 5*time.Second, 100*time.Millisecond, func() (bool, error) {
		if _, err := os.Stat(nbd + "p1"); err == nil {
			return true, nil
		}
		return false, nil
	}); err != nil {
		return fmt.Errorf("wait for partition on %s: %w", nbd, err)
	}
	return nil
}

func cleanupNBDMount(ctx context.Context, mnt string, mounted bool) error {
	if !mounted {
		if err := os.Remove(mnt); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove mount dir %s: %w", mnt, err)
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), nbdCleanupTimeout)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "umount", mnt).CombinedOutput(); err != nil {
		return fmt.Errorf("unmount %s (output: %s): %w", mnt, out, err)
	}
	if err := os.Remove(mnt); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove mount dir %s: %w", mnt, err)
	}
	return nil
}

// isFileHeld matches the daemonized qemu-nbd server's cmdline — cheaper than scanning fd tables, and that server is the only holder to wait out.
func isFileHeld(ocPath string) (bool, error) {
	pids, err := utils.FindVMMByCmdline("qemu-nbd", ocPath)
	return len(pids) > 0, err
}

// cleanupNBDForPath terminates qemu-nbd holders of path; PID identity is verified so a reused PID is never hit.
func cleanupNBDForPath(ctx context.Context, path string) error {
	return procutil.TerminateByCmdline(ctx, "qemu-nbd", path, time.Second)
}

// holds the nbd lease; --fork or qemu-nbd stays foreground and the caller never reaches mount/patch.
func connectFreeNBD(ctx context.Context, ocPath string) (*nbdConnection, error) {
	var lastErr error
	pidFile := ocPath + ".nbd.pid"
	_ = os.Remove(pidFile)
	cmdlines, err := readProcCmdlines("/proc")
	if err != nil {
		return nil, err
	}
	for i := range nbdDeviceCount {
		nbd := fmt.Sprintf("/dev/nbd%d", i)
		if _, err := os.Stat(nbd); err != nil {
			continue
		}
		if _, err := os.Stat(fmt.Sprintf("/sys/block/nbd%d/pid", i)); !os.IsNotExist(err) {
			continue
		}
		if processReferencesBlockDevice(cmdlines, nbd) {
			continue
		}
		out, cerr := exec.CommandContext(ctx, "qemu-nbd", qemuNBDConnectArgs(nbd, pidFile, ocPath)...).CombinedOutput()
		if cerr == nil {
			pid, err := utils.ReadPIDFile(pidFile)
			if err == nil {
				return &nbdConnection{device: nbd, pid: pid, pidFile: pidFile, ocPath: ocPath}, nil
			}
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), nbdCleanupTimeout)
			_ = exec.CommandContext(cleanupCtx, "qemu-nbd", "--disconnect", nbd).Run()
			_ = cleanupNBDForPath(cleanupCtx, ocPath)
			cancel()
			return nil, fmt.Errorf("read qemu-nbd pid file %s: %w", pidFile, err)
		}
		lastErr = fmt.Errorf("connect qemu-nbd %s (output: %s): %w", nbd, out, cerr)
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), nbdCleanupTimeout)
		_ = cleanupNBDForPath(cleanupCtx, ocPath)
		cancel()
		if ctx.Err() != nil {
			return nil, errors.Join(lastErr, ctx.Err())
		}
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, errors.New("no free /dev/nbd device (is the nbd module loaded)")
}

func readProcCmdlines(procRoot string) ([]string, error) {
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", procRoot, err)
	}
	var cmdlines []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		cmdlinePath := filepath.Join(procRoot, entry.Name(), "cmdline")
		cmdline, err := os.ReadFile(cmdlinePath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("read %s: %w", cmdlinePath, err)
		}
		cmdlines = append(cmdlines, string(cmdline))
	}
	return cmdlines, nil
}

// a device can stay held by a blocked userspace op after the kernel drops its sysfs pid; reusing it wedges the next attach.
func processReferencesBlockDevice(cmdlines []string, device string) bool {
	for _, cmdline := range cmdlines {
		for arg := range strings.SplitSeq(cmdline, "\x00") {
			if nbdArgReferencesDevice(arg, device) {
				return true
			}
		}
	}
	return false
}

func nbdArgReferencesDevice(arg, device string) bool {
	if arg == device || arg == "--connect="+device {
		return true
	}
	partition := strings.TrimPrefix(arg, device+"p")
	if partition == arg || partition == "" {
		return false
	}
	_, err := strconv.Atoi(partition)
	return err == nil
}

func qemuNBDConnectArgs(nbd, pidFile, ocPath string) []string {
	return []string{"--fork", "--pid-file=" + pidFile, "--connect=" + nbd, "-f", "qcow2", ocPath}
}

func patchPlist(path string, sm *SMBIOS) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read config.plist: %w", err)
	}
	var cfg map[string]any
	if _, perr := plist.Unmarshal(b, &cfg); perr != nil {
		return fmt.Errorf("parse config.plist: %w", perr)
	}
	if sm != nil {
		rom, derr := hex.DecodeString(sm.ROM)
		if derr != nil {
			return fmt.Errorf("decode ROM: %w", derr)
		}
		pi := ensureSubMap(cfg, "PlatformInfo")
		pi["Automatic"] = true
		pi["UpdateSMBIOS"] = true
		pi["UpdateNVRAM"] = true
		g := ensureSubMap(pi, "Generic")
		g["SystemProductName"] = sm.Model
		g["SystemSerialNumber"] = sm.Serial
		g["MLB"] = sm.MLB
		g["SystemUUID"] = sm.UUID
		g["ROM"] = rom
		g["SpoofVendor"] = true
	}
	// ShowPicker=false: a visible picker can't be driven headlessly — OpenCanopy cancels its Timeout on stray USB-enumeration input and waits forever
	boot := ensureSubMap(ensureSubMap(cfg, "Misc"), "Boot")
	boot["ShowPicker"] = false
	boot["HideAuxiliary"] = true
	boot["Timeout"] = uint64(5)
	ensureSubMap(ensureSubMap(cfg, "UEFI"), "Quirks")["RequestBootVarRouting"] = true
	out, err := plist.MarshalIndent(cfg, plist.XMLFormat, "\t")
	if err != nil {
		return fmt.Errorf("encode config.plist: %w", err)
	}
	return os.WriteFile(path, out, 0o600)
}

func ensureSubMap(m map[string]any, key string) map[string]any {
	if sub, ok := m[key].(map[string]any); ok {
		return sub
	}
	sub := map[string]any{}
	m[key] = sub
	return sub
}
