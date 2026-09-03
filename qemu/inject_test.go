package qemu

import (
	"context"
	"encoding/hex"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"howett.net/plist"
)

const sampleConfig = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Misc</key>
	<dict>
		<key>Security</key>
		<dict>
			<key>Vault</key>
			<string>Optional</string>
		</dict>
	</dict>
	<key>PlatformInfo</key>
	<dict>
		<key>Automatic</key>
		<true/>
		<key>UpdateSMBIOSMode</key>
		<string>Create</string>
		<key>Generic</key>
		<dict>
			<key>SystemProductName</key>
			<string>iMac19,1</string>
			<key>SystemSerialNumber</key>
			<string>W00000000001</string>
			<key>MLB</key>
			<string>M0000000000000001</string>
			<key>SystemUUID</key>
			<string>00000000-0000-0000-0000-000000000000</string>
			<key>ROM</key>
			<data>ESIzRFVm</data>
			<key>SpoofVendor</key>
			<true/>
		</dict>
	</dict>
</dict>
</plist>
`

func TestQemuNBDConnectArgsForkAndTrackServer(t *testing.T) {
	want := []string{"--fork", "--pid-file=/state/vm/OpenCore.qcow2.nbd.pid", "--connect=/dev/nbd3", "-f", "qcow2", "/state/vm/OpenCore.qcow2"}
	if got := qemuNBDConnectArgs("/dev/nbd3", "/state/vm/OpenCore.qcow2.nbd.pid", "/state/vm/OpenCore.qcow2"); !slices.Equal(got, want) {
		t.Fatalf("qemu-nbd args = %v, want %v", got, want)
	}
}

func TestWithNBDLeaseSerializesConcurrentInjectors(t *testing.T) {
	const workers = 50
	lockPath := filepath.Join(t.TempDir(), "nbd.lock")
	var active, maxActive atomic.Int32
	errCh := make(chan error, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			errCh <- withNBDLease(t.Context(), lockPath, func() error {
				n := active.Add(1)
				defer active.Add(-1)
				for old := maxActive.Load(); n > old && !maxActive.CompareAndSwap(old, n); old = maxActive.Load() {
				}
				time.Sleep(time.Millisecond)
				return nil
			})
		})
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := maxActive.Load(); got != 1 {
		t.Fatalf("maximum concurrent NBD transactions = %d, want 1", got)
	}
}

func TestWithNBDLeaseHonorsCancellation(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "nbd.lock")
	held := make(chan struct{})
	release := make(chan struct{})
	go func() {
		_ = withNBDLease(t.Context(), lockPath, func() error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if err := withNBDLease(ctx, lockPath, func() error { return nil }); err == nil {
		t.Fatal("waiting NBD lease ignored context cancellation")
	}
	close(release)
}

func TestProcessReferencesBlockDevice(t *testing.T) {
	procRoot := t.TempDir()
	writeCmdline := func(pid, command string, args ...string) {
		t.Helper()
		dir := filepath.Join(procRoot, pid)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		argv := append([]string{command}, args...)
		data := append([]byte(strings.Join(argv, "\x00")), 0)
		if err := os.WriteFile(filepath.Join(dir, "cmdline"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	writeCmdline("101", "partprobe", "/dev/nbd0")
	writeCmdline("102", "mount", "/dev/nbd1p1", "/mnt")
	writeCmdline("103", "qemu-nbd", "--connect=/dev/nbd2", "disk.qcow2")
	writeCmdline("104", "helper", "/dev/nbd01")

	for _, device := range []string{"/dev/nbd0", "/dev/nbd1", "/dev/nbd2"} {
		cmdlines, err := readProcCmdlines(procRoot)
		if err != nil {
			t.Fatalf("readProcCmdlines: %v", err)
		}
		busy := processReferencesBlockDevice(cmdlines, device)
		if !busy {
			t.Errorf("processReferencesBlockDevice(%s) = false, want true", device)
		}
	}
	cmdlines, err := readProcCmdlines(procRoot)
	if err != nil {
		t.Fatal(err)
	}
	busy := processReferencesBlockDevice(cmdlines, "/dev/nbd3")
	if busy {
		t.Error("unreferenced /dev/nbd3 reported busy")
	}
}

func TestPatchPlist(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.plist")
	if err := os.WriteFile(p, []byte(sampleConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	sm := SMBIOS{
		Model: "iMac19,1", Serial: "ABC123DEF456", MLB: "AABBCCDDEEFF00112",
		UUID: "11223344-5566-4788-99AA-BBCCDDEEFF00", ROM: "020406080a0c",
	}
	if err := patchPlist(p, &sm); err != nil {
		t.Fatalf("patchPlist: %v", err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if _, err := plist.Unmarshal(b, &cfg); err != nil {
		t.Fatalf("patched config.plist no longer parses: %v", err)
	}
	pi, ok := cfg["PlatformInfo"].(map[string]any)
	if !ok {
		t.Fatal("PlatformInfo missing after patch")
	}
	g, ok := pi["Generic"].(map[string]any)
	if !ok {
		t.Fatal("Generic missing after patch")
	}
	if g["SystemSerialNumber"] != sm.Serial || g["MLB"] != sm.MLB || g["SystemUUID"] != sm.UUID {
		t.Errorf("identity not injected: serial=%v mlb=%v uuid=%v", g["SystemSerialNumber"], g["MLB"], g["SystemUUID"])
	}
	if pi["Automatic"] != true {
		t.Errorf("Automatic = %v, want true", pi["Automatic"])
	}
	if rom, _ := g["ROM"].([]byte); hex.EncodeToString(rom) != sm.ROM {
		t.Errorf("ROM = %x, want %s", rom, sm.ROM)
	}
	if pi["UpdateSMBIOSMode"] != "Create" {
		t.Errorf("unrelated PlatformInfo key not preserved: UpdateSMBIOSMode = %v", pi["UpdateSMBIOSMode"])
	}
	misc, ok := cfg["Misc"].(map[string]any)
	if !ok {
		t.Fatal("unrelated top-level key Misc dropped")
	}
	sec, _ := misc["Security"].(map[string]any)
	if sec == nil || sec["Vault"] != "Optional" {
		t.Errorf("Misc.Security.Vault not preserved: %v", sec)
	}
}
