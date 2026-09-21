package vm

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/cocoonstack/cocoon-macos/home"
	"github.com/cocoonstack/cocoon/images/cloudimg"
	"github.com/cocoonstack/cocoon/progress"
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

func TestValidateMemory(t *testing.T) {
	for _, tt := range []struct {
		mem     string
		wantErr bool
	}{
		{mem: "8192"},
		{mem: "512"},
		{mem: "8G", wantErr: true},
		{mem: "819x", wantErr: true},
		{mem: "0", wantErr: true},
		{mem: "", wantErr: true},
	} {
		t.Run(tt.mem, func(t *testing.T) {
			err := validateMemory(tt.mem)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateMemory(%q) error = %v, wantErr %v", tt.mem, err, tt.wantErr)
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

	if err := Start(cmd, []string{"macos-demo"}); err != nil {
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

	if err := Start(cmd, []string{"macos-demo"}); err != nil {
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

func TestCloneImageBaseDerivesTheBlobPath(t *testing.T) {
	cmd := newLifecycleTestCommand(t, t.TempDir())
	hex := strings.Repeat("a", 64)
	blob := cloudimg.NewConfig(home.Dir(cmd), 0).BlobPath(hex)
	src := &record{Name: "src", Image: "img", ImageDigest: "sha256:" + hex}
	if got := cloneImageBase(cmd, src); got != "img" {
		t.Fatalf("cloneImageBase before the blob exists = %q, want the ref", got)
	}
	if err := os.MkdirAll(filepath.Dir(blob), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(blob, []byte("base"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := cloneImageBase(cmd, src); got != blob {
		t.Fatalf("cloneImageBase = %q, want the digest's blob %q", got, blob)
	}
}

func TestCloneImageBaseIsTheBlobTheSourceWasBuiltOn(t *testing.T) {
	if _, err := exec.LookPath("qemu-img"); err != nil {
		t.Skip("the store's import inspects the file with qemu-img")
	}
	cmd := newLifecycleTestCommand(t, t.TempDir())
	ctx := t.Context()
	store, err := home.OpenStore(ctx, cmd)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	importAs := func(content string) {
		p := filepath.Join(t.TempDir(), content+".raw")
		if err := os.WriteFile(p, []byte(strings.Repeat(content, 1024)), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := store.Import(ctx, "img", progress.Nop, p); err != nil {
			t.Fatalf("import %s: %v", content, err)
		}
	}
	importAs("first")
	firstBase, firstDigest, err := resolveBase(ctx, cmd, "img", "n")
	if err != nil || firstDigest == "" {
		t.Fatalf("resolveBase(img) = %q, %q, %v", firstBase, firstDigest, err)
	}
	importAs("second")

	src := &record{Name: "src", Image: "img", ImageDigest: firstDigest}
	if got := cloneImageBase(cmd, src); got != firstBase {
		t.Fatalf("cloneImageBase after a re-pull of the same ref = %q, want the source's blob %q", got, firstBase)
	}
	gone := &record{Name: "src", Image: "img", ImageDigest: "sha256:" + strings.Repeat("0", 64)}
	if got := cloneImageBase(cmd, gone); got != "img" {
		t.Errorf("cloneImageBase with the blob gone = %q, want the ref", got)
	}
	if got := cloneImageBase(cmd, &record{Image: "/images/base.qcow2"}); got != "/images/base.qcow2" {
		t.Errorf("cloneImageBase without a digest = %q, want the ref", got)
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
