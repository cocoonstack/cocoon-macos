package qemu

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/projecteru2/core/log"
	"howett.net/plist"

	"github.com/cocoonstack/cocoon/utils"
)

// InjectConfig mounts the OpenCore qcow2 via qemu-nbd (the only way to edit a FAT partition
// inside a qcow2; needs root + the nbd module) and patches config.plist with a per-VM identity.
func InjectConfig(ctx context.Context, ocPath string, sm *SMBIOS) error {
	_ = exec.CommandContext(ctx, "modprobe", "nbd", "max_part=8").Run()
	nbd, err := connectFreeNBD(ctx, ocPath)
	if err != nil {
		return err
	}
	// cleanup stays on plain exec.Command: it must still run after ctx cancellation
	defer disconnectNBD(ctx, nbd, ocPath)
	waitForPart(ctx, nbd)
	mnt, err := os.MkdirTemp("", "oc-efi-")
	if err != nil {
		return fmt.Errorf("create mount dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(mnt) }()
	var mountErr error
	for _, p := range []string{nbd + "p1", nbd + "p2", nbd} {
		if mountErr = exec.CommandContext(ctx, "mount", p, mnt).Run(); mountErr == nil {
			break
		}
	}
	if mountErr != nil {
		return fmt.Errorf("mount OpenCore EFI partition on %s: %w", nbd, mountErr)
	}
	defer func() { _ = exec.Command("umount", mnt).Run() }()
	return patchPlist(filepath.Join(mnt, "EFI", "OC", "config.plist"), sm)
}

// waitForPart blocks until the kernel's async partition scan exposes nbdXp1 for the mount.
func waitForPart(ctx context.Context, nbd string) {
	_ = utils.WaitFor(ctx, 5*time.Second, 100*time.Millisecond, func() (bool, error) {
		if _, err := os.Stat(nbd + "p1"); err == nil {
			return true, nil
		}
		_ = exec.CommandContext(ctx, "partprobe", nbd).Run()
		return false, nil
	})
}

// disconnectNBD waits out qemu-nbd's asynchronous release, or the qemu launch races in and fails
// with "Failed to get shared write lock".
func disconnectNBD(ctx context.Context, nbd, ocPath string) {
	_ = exec.Command("qemu-nbd", "--disconnect", nbd).Run()
	if err := utils.WaitFor(ctx, 10*time.Second, 100*time.Millisecond, func() (bool, error) {
		return !isFileHeld(ocPath), nil
	}); err != nil {
		log.WithFunc("qemu.disconnectNBD").Warnf(ctx, "qcow2 %s still held after nbd disconnect: %v", ocPath, err)
	}
}

// isFileHeld matches the daemonized qemu-nbd server's cmdline — cheaper than scanning fd tables,
// and that server is the only holder to wait out.
func isFileHeld(ocPath string) bool {
	pids, err := utils.FindVMMByCmdline("qemu-nbd", ocPath)
	return err == nil && len(pids) > 0
}

// connectFreeNBD claims a device by connecting: the connect itself is the exclusive operation,
// so a race with another VM create just advances to the next candidate.
func connectFreeNBD(ctx context.Context, ocPath string) (string, error) {
	var lastErr error
	for i := range 16 {
		nbd := fmt.Sprintf("/dev/nbd%d", i)
		if _, err := os.Stat(nbd); err != nil {
			continue
		}
		if _, err := os.Stat(fmt.Sprintf("/sys/block/nbd%d/pid", i)); !os.IsNotExist(err) {
			continue
		}
		out, cerr := exec.CommandContext(ctx, "qemu-nbd", "--connect="+nbd, "-f", "qcow2", ocPath).CombinedOutput()
		if cerr == nil {
			return nbd, nil
		}
		lastErr = fmt.Errorf("connect qemu-nbd %s (output: %s): %w", nbd, out, cerr)
	}
	if lastErr != nil {
		return "", lastErr
	}
	return "", errors.New("no free /dev/nbd device (is the nbd module loaded)")
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
	// ShowPicker=false: a visible picker can't be driven headlessly — OpenCanopy cancels its
	// Timeout on stray USB-enumeration input and waits forever
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
