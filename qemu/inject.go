package qemu

import (
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"howett.net/plist"
)

// InjectSMBIOS writes the identity into a per-VM OpenCore config.plist (PlatformInfo/Generic)
// by mounting the OpenCore qcow2 via qemu-nbd. Requires root + the nbd kernel module. The
// shipped OpenCore has Automatic=true + Vault=Optional, so Generic is the SMBIOS source and
// no vault signature rejects the edit.
func InjectSMBIOS(ocPath string, s SMBIOS) error {
	_ = exec.Command("modprobe", "nbd", "max_part=8").Run()
	nbd, err := freeNBD()
	if err != nil {
		return err
	}
	if out, cerr := exec.Command("qemu-nbd", "--connect="+nbd, "-f", "qcow2", ocPath).CombinedOutput(); cerr != nil {
		return fmt.Errorf("qemu-nbd connect %s: %v: %s", nbd, cerr, out)
	}
	defer func() { _ = exec.Command("qemu-nbd", "--disconnect", nbd).Run() }()
	time.Sleep(2 * time.Second)
	_ = exec.Command("partprobe", nbd).Run()
	mnt, err := os.MkdirTemp("", "oc-efi-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(mnt) }()
	mounted := false
	for _, p := range []string{nbd + "p1", nbd + "p2", nbd} {
		if exec.Command("mount", p, mnt).Run() == nil {
			mounted = true
			break
		}
	}
	if !mounted {
		return fmt.Errorf("mount OpenCore EFI partition on %s failed", nbd)
	}
	defer func() { _ = exec.Command("umount", mnt).Run() }()
	return patchPlist(filepath.Join(mnt, "EFI", "OC", "config.plist"), s)
}

func freeNBD() (string, error) {
	for i := range 16 {
		if _, err := os.Stat(fmt.Sprintf("/dev/nbd%d", i)); err != nil {
			continue
		}
		if _, err := os.Stat(fmt.Sprintf("/sys/block/nbd%d/pid", i)); os.IsNotExist(err) {
			return fmt.Sprintf("/dev/nbd%d", i), nil
		}
	}
	return "", fmt.Errorf("no free /dev/nbd device (is the nbd module loaded?)")
}

func patchPlist(path string, s SMBIOS) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read config.plist: %w", err)
	}
	var cfg map[string]any
	if _, perr := plist.Unmarshal(b, &cfg); perr != nil {
		return fmt.Errorf("parse config.plist: %w", perr)
	}
	rom, err := hex.DecodeString(s.ROM)
	if err != nil {
		return fmt.Errorf("decode ROM: %w", err)
	}
	pi := subMap(cfg, "PlatformInfo")
	pi["Automatic"] = true
	pi["UpdateSMBIOS"] = true
	pi["UpdateNVRAM"] = true
	g := subMap(pi, "Generic")
	g["SystemProductName"] = s.Model
	g["SystemSerialNumber"] = s.Serial
	g["MLB"] = s.MLB
	g["SystemUUID"] = s.UUID
	g["ROM"] = rom
	g["SpoofVendor"] = true
	out, err := plist.MarshalIndent(cfg, plist.XMLFormat, "\t")
	if err != nil {
		return fmt.Errorf("encode config.plist: %w", err)
	}
	return os.WriteFile(path, out, 0o644)
}

func subMap(m map[string]any, key string) map[string]any {
	if sub, ok := m[key].(map[string]any); ok {
		return sub
	}
	sub := map[string]any{}
	m[key] = sub
	return sub
}
