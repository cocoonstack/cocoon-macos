package vm

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestValidateMacOSCPUs(t *testing.T) {
	for _, tt := range []struct {
		cpus    int
		wantErr bool
	}{
		{cpus: -2, wantErr: true},
		{cpus: 0, wantErr: true},
		{cpus: 1, wantErr: true},
		{cpus: 2},
		{cpus: 3, wantErr: true},
		{cpus: 4},
	} {
		t.Run(strconv.Itoa(tt.cpus), func(t *testing.T) {
			err := validateMacOSCPUs(tt.cpus)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateMacOSCPUs(%d) error = %v, wantErr %v", tt.cpus, err, tt.wantErr)
			}
			if err != nil && !strings.Contains(err.Error(), "positive even number") {
				t.Fatalf("validateMacOSCPUs(%d) error = %v", tt.cpus, err)
			}
		})
	}
}

func TestValidateTapFlag(t *testing.T) {
	for _, tt := range []struct {
		name, netMode, tap string
		wantErr            bool
	}{
		{"tap mode with a tap", netTAP, "tap0", false},
		{"tap mode without a tap", netTAP, "", false},
		{"user mode without a tap", "", "", false},
		{"default mode with a tap", "", "tap0", true},
		{"user mode with a tap", netUser, "tap0", true},
		{"cni mode with a tap", netCNI, "tap0", true},
		{"bridge mode with a tap", netBridge, "tap0", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := validateTapFlag(tt.netMode, tt.tap)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateTapFlag(%q, %q) error = %v, wantErr %v", tt.netMode, tt.tap, err, tt.wantErr)
			}
			if err != nil && !strings.Contains(err.Error(), "--tap requires --net tap") {
				t.Fatalf("validateTapFlag(%q, %q) error = %v", tt.netMode, tt.tap, err)
			}
		})
	}
}

func TestStartAlreadyRunningIsIdempotent(t *testing.T) {
	stateDir := t.TempDir()
	vmDir := filepath.Join(stateDir, "vms", "macos-demo")
	if err := os.MkdirAll(vmDir, 0o755); err != nil {
		t.Fatal(err)
	}
	disk := filepath.Join(vmDir, "disk.qcow2")
	rec := &record{Name: "macos-demo", Disk: disk, PID: spawnFakeQEMU(t, disk), VNCDisp: 1}
	if err := saveRec(vmDir, rec); err != nil {
		t.Fatal(err)
	}
	cmd := newLifecycleTestCommand(t, stateDir)
	cmd.Flags().Int("vnc", -1, "")
	cmd.Flags().String("vnc-password", "", "")
	if err := cmd.Flags().Set("vnc", "2"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Flags().Set("vnc-password", "newpass"); err != nil {
		t.Fatal(err)
	}

	if err := NewHandler().Start(cmd, []string{"macos-demo"}); err != nil {
		t.Fatalf("duplicate start must adopt the live qemu: %v", err)
	}
	got, err := loadRec(vmDir)
	if err != nil {
		t.Fatal(err)
	}
	if got.PID != rec.PID || got.VNCDisp != 1 {
		t.Fatalf("live record changed: pid=%d vnc=%d, want pid=%d vnc=1", got.PID, got.VNCDisp, rec.PID)
	}
}

func TestStartAdoptsQEMUWhenRecordPIDWasNotCommitted(t *testing.T) {
	stateDir := t.TempDir()
	vmDir, pid := startUnrecordedQEMU(t, stateDir, "macos-demo")
	cmd := newLifecycleTestCommand(t, stateDir)
	cmd.Flags().Int("vnc", -1, "")
	cmd.Flags().String("vnc-password", "", "")

	if err := NewHandler().Start(cmd, []string{"macos-demo"}); err != nil {
		t.Fatalf("Start must adopt the already-running QEMU: %v", err)
	}
	got, err := loadRec(vmDir)
	if err != nil {
		t.Fatal(err)
	}
	if got.PID != pid {
		t.Fatalf("adopted PID = %d, want %d", got.PID, pid)
	}
}

func TestCloneOpenCoreBase(t *testing.T) {
	tests := []struct {
		name string
		src  *record
		want string
	}{
		{"recorded base is inherited, not the source overlay", &record{OpenCore: "/vms/src/OpenCore.qcow2", OpenCoreBase: "/fw/OpenCore.qcow2"}, "/fw/OpenCore.qcow2"},
		{"no identity: OpenCore is itself the base", &record{OpenCore: "/fw/OpenCore.qcow2"}, "/fw/OpenCore.qcow2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := cloneOpenCoreBase(&cobra.Command{}, tt.src)
			if err != nil {
				t.Fatalf("cloneOpenCoreBase: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestImagesToSnapshot(t *testing.T) {
	tests := []struct {
		name string
		rec  *record
		want []string
	}{
		{"raw nvram captures disk only", &record{Disk: "/v/disk.qcow2", OVMFVars: "/v/OVMF_VARS.fd"}, []string{"/v/disk.qcow2"}},
		{"qcow2 nvram captures both", &record{Disk: "/v/disk.qcow2", OVMFVars: "/v/OVMF_VARS.qcow2"}, []string{"/v/disk.qcow2", "/v/OVMF_VARS.qcow2"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := imagesToSnapshot(tt.rec); !slices.Equal(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPrepareNetNoProvision(t *testing.T) {
	tests := []struct {
		name                        string
		rec                         *record
		wantTap, wantNetns, wantMAC string
	}{
		{"user-mode", &record{NetMode: "user", MAC: "aa:bb:cc:dd:ee:ff"}, "", "", "aa:bb:cc:dd:ee:ff"},
		{"pre-created tap", &record{NetMode: "tap", Tap: "tap0", MAC: "aa:bb:cc:dd:ee:ff"}, "tap0", "", "aa:bb:cc:dd:ee:ff"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := &cobra.Command{}
			cmd.SetContext(t.Context())
			tap, netns, mac, err := prepareNet(cmd, tt.rec)
			if err != nil {
				t.Fatalf("prepareNet: %v", err)
			}
			if tap != tt.wantTap || netns != tt.wantNetns || mac != tt.wantMAC {
				t.Errorf("got tap=%q netns=%q mac=%q; want %q %q %q", tap, netns, mac, tt.wantTap, tt.wantNetns, tt.wantMAC)
			}
		})
	}
}
