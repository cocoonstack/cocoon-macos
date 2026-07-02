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

// InjectConfig mounts the OpenCore qcow2 and patches its config.plist with a per-VM SMBIOS identity.
//
// qemu-nbd is the only way to edit a FAT partition inside a qcow2, so this needs root and the nbd
// module. The shipped OpenCore runs Automatic=true + Vault=Optional, so Generic is the SMBIOS source
// and no vault signature rejects the edit.
func InjectConfig(ctx context.Context, ocPath string, sm *SMBIOS) error {
	_ = exec.CommandContext(ctx, "modprobe", "nbd", "max_part=8").Run()
	nbd, err := findFreeNBD()
	if err != nil {
		return err
	}
	if out, cerr := exec.CommandContext(ctx, "qemu-nbd", "--connect="+nbd, "-f", "qcow2", ocPath).CombinedOutput(); cerr != nil {
		return fmt.Errorf("connect qemu-nbd %s (output: %s): %w", nbd, out, cerr)
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

// waitForPart blocks until the kernel has scanned the qcow2's partition table (nbd partition
// creation is asynchronous after --connect), so the mount below sees nbdXp1.
func waitForPart(ctx context.Context, nbd string) {
	_ = utils.WaitFor(ctx, 5*time.Second, 100*time.Millisecond, func() (bool, error) {
		if _, err := os.Stat(nbd + "p1"); err == nil {
			return true, nil
		}
		_ = exec.CommandContext(ctx, "partprobe", nbd).Run()
		return false, nil
	})
}

// disconnectNBD tears down the qemu-nbd mapping and waits until the qcow2 is no longer held.
// qemu-nbd --disconnect releases the device asynchronously and the server pid is not
// /sys/block/nbdX/pid, so returning early would let the qemu launch race in and fail with
// "Failed to get shared write lock".
func disconnectNBD(ctx context.Context, nbd, ocPath string) {
	_ = exec.Command("qemu-nbd", "--disconnect", nbd).Run()
	if err := utils.WaitFor(ctx, 10*time.Second, 100*time.Millisecond, func() (bool, error) {
		return !isFileHeld(ocPath), nil
	}); err != nil {
		// surfaces the failure the godoc warns about instead of leaving no diagnostic trail
		log.WithFunc("qemu.disconnectNBD").Warnf(ctx, "qcow2 %s still held after nbd disconnect: %v", ocPath, err)
	}
}

// isFileHeld reports whether a qemu-nbd server still has ocPath open, by matching its cmdline
// (one /proc walk) — cheaper than scanning every process's fd table, and the daemonized server
// is the only holder to wait out.
func isFileHeld(ocPath string) bool {
	pids, err := utils.FindVMMByCmdline("qemu-nbd", ocPath)
	return err == nil && len(pids) > 0
}

func findFreeNBD() (string, error) {
	for i := range 16 {
		if _, err := os.Stat(fmt.Sprintf("/dev/nbd%d", i)); err != nil {
			continue
		}
		if _, err := os.Stat(fmt.Sprintf("/sys/block/nbd%d/pid", i)); os.IsNotExist(err) {
			return fmt.Sprintf("/dev/nbd%d", i), nil
		}
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
	// Auto-boot the installed macOS: hide the EFI/recovery aux entry + a short timeout, so the VM
	// never stalls at the OpenCore picker (which can't be driven reliably headlessly — a missed
	// sendkey boots the dead EFI entry and the VM never reaches macOS).
	boot := ensureSubMap(ensureSubMap(cfg, "Misc"), "Boot")
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
