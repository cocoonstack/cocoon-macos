package vm

import (
	"cmp"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/cocoonstack/cocoon-macos/home"
	"github.com/cocoonstack/cocoon-macos/qemu"
	"github.com/cocoonstack/cocoon/utils"
)

func TestRequestedVMNameFollowsCocoonsRule(t *testing.T) {
	for _, tt := range []struct {
		flag, fallback, want, wantErr string
	}{
		{flag: "macos-demo", want: "macos-demo"},
		{flag: "my.vm_1", want: "my.vm_1"},
		{fallback: "macos-20260921-153000", want: "macos-20260921-153000"},
		{flag: "a", want: "a"},
		{flag: strings.Repeat("n", 63), want: strings.Repeat("n", 63)},
		{flag: strings.Repeat("n", 64), wantErr: "must match"},
		{fallback: strings.Repeat("s", 60) + "-clone-153000", wantErr: "pass --name"},
		{flag: "-demo", wantErr: "must match"},
		{flag: ".demo", wantErr: "must match"},
		{flag: "demo one", wantErr: "must match"},
		{flag: "demo,one", wantErr: "must match"},
		{flag: "nested/demo", wantErr: "must match"},
	} {
		t.Run(cmp.Or(tt.flag, tt.fallback), func(t *testing.T) {
			cmd := &cobra.Command{}
			cmd.Flags().String("name", tt.flag, "")
			got, err := requestedVMName(cmd, tt.fallback)
			if got != tt.want || (tt.wantErr == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("requestedVMName(%q, %q) = %q, %v; want %q, error containing %q", tt.flag, tt.fallback, got, err, tt.want, tt.wantErr)
			}
		})
	}
}

func TestSnapshotAdoptsQEMUWhenRecordPIDWasNotCommitted(t *testing.T) {
	stateDir := t.TempDir()
	vmDir, pid := startUnrecordedQEMU(t, stateDir, "macos-demo")
	cmd := newLifecycleTestCommand(t, stateDir)
	cmd.Flags().String("tag", "", "")

	err := Snapshot(cmd, []string{"macos-demo"})
	if err == nil || !strings.Contains(err.Error(), "is running") {
		t.Fatalf("Snapshot error = %v, want running VM rejection", err)
	}
	r, err := loadRec(vmDir)
	if err != nil {
		t.Fatal(err)
	}
	if r.PID != pid {
		t.Errorf("adopted PID = %d, want %d", r.PID, pid)
	}
}

func TestSnapshotRefusesDuplicateTag(t *testing.T) {
	stateDir := t.TempDir()
	cmd := newLifecycleTestCommand(t, stateDir)
	cmd.Flags().String("tag", "", "")
	if err := cmd.Flags().Set("tag", "base"); err != nil {
		t.Fatal(err)
	}
	vmDir, err := home.VMDir(cmd, "macos-demo")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(vmDir, 0o755); err != nil {
		t.Fatal(err)
	}
	r := &record{Name: "macos-demo", Disk: filepath.Join(vmDir, "disk.qcow2"), VNCDisp: -1, Snapshots: []string{"base"}}
	if err := saveRec(vmDir, r); err != nil {
		t.Fatal(err)
	}

	err = Snapshot(cmd, []string{"macos-demo"})
	if err == nil || !strings.Contains(err.Error(), "already has snapshot") {
		t.Fatalf("Snapshot error = %v, want the duplicate tag refusal", err)
	}
}

func TestRMAdoptsQEMUWhenRecordPIDWasNotCommitted(t *testing.T) {
	stateDir := t.TempDir()
	vmDir, _ := startUnrecordedQEMU(t, stateDir, "macos-demo")
	cmd := newLifecycleTestCommand(t, stateDir)
	cmd.Flags().Bool("force", false, "")
	if err := cmd.Flags().Set("force", "true"); err != nil {
		t.Fatal(err)
	}

	if err := RM(cmd, []string{"macos-demo"}); err != nil {
		t.Fatalf("RM: %v", err)
	}
	if _, err := os.Stat(vmDir); !os.IsNotExist(err) {
		t.Errorf("VM directory still exists: %v", err)
	}
}

func TestRestoreRefusesRunningPasswordedVNC(t *testing.T) {
	stateDir := t.TempDir()
	vmDir, pid := startUnrecordedQEMU(t, stateDir, "macos-demo")
	r, err := loadRec(vmDir)
	if err != nil {
		t.Fatal(err)
	}
	r.VNCDisp, r.VNCPassSet = 7, true
	if err := saveRec(vmDir, r); err != nil {
		t.Fatal(err)
	}
	cmd := newLifecycleTestCommand(t, stateDir)
	cmd.Flags().String("tag", "", "")
	cmd.Flags().Bool("force", true, "")
	cmd.Flags().String("vnc-password", "", "")

	err = Restore(cmd, []string{"macos-demo"})
	if err == nil || !strings.Contains(err.Error(), "password-gated VNC") {
		t.Fatalf("Restore error = %v, want the password-gated VNC refusal", err)
	}
	if !utils.VerifyProcessCmdline(pid, qemuBinary, filepath.Join(vmDir, "disk.qcow2")) {
		t.Error("qemu was terminated by a refused restore")
	}
}

func TestRestoreForceStopsVNCProxyWhenApplyIsRefused(t *testing.T) {
	stateDir := t.TempDir()
	vmDir, _ := startUnrecordedQEMU(t, stateDir, "macos-demo")
	r, err := loadRec(vmDir)
	if err != nil {
		t.Fatal(err)
	}
	r.VNCDisp = 7
	if err := saveRec(vmDir, r); err != nil {
		t.Fatal(err)
	}
	spawnTestProxy(t, vmDir)
	cmd := newLifecycleTestCommand(t, stateDir)
	cmd.Flags().String("tag", "", "")
	cmd.Flags().Bool("force", true, "")
	cmd.Flags().String("vnc-password", "", "")

	err = Restore(cmd, []string{"macos-demo"})
	if err == nil || !strings.Contains(err.Error(), "no snapshots") {
		t.Fatalf("Restore error = %v, want the no-snapshots refusal after the stop", err)
	}
	if _, err := os.Stat(filepath.Join(vmDir, vncProxyPID)); !os.IsNotExist(err) {
		t.Errorf("proxy pid file survived the refused restore: %v", err)
	}
	if err := utils.WaitFor(t.Context(), 2*time.Second, 10*time.Millisecond, func() (bool, error) {
		return !vncProxyRunning(vmDir), nil
	}); err != nil {
		t.Errorf("proxy still running after the refused restore: %v", err)
	}
}

func TestCloneRejectsRunningSource(t *testing.T) {
	stateDir := t.TempDir()
	startUnrecordedQEMU(t, stateDir, "macos-src")
	cmd := newLifecycleTestCommand(t, stateDir)
	cmd.Flags().String("name", "", "")
	if err := cmd.Flags().Set("name", "macos-clone"); err != nil {
		t.Fatal(err)
	}

	err := Clone(cmd, []string{"macos-src"})
	if err == nil || !strings.Contains(err.Error(), "stop it before cloning") {
		t.Fatalf("Clone error = %v, want running source rejection", err)
	}
	dir, err := home.VMDir(cmd, "macos-clone")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("clone directory exists: %v", err)
	}
}

func TestStopAdoptsQEMUWithCommaInVMPath(t *testing.T) {
	stateDir := t.TempDir()
	name := "macos,demo"
	vmDir, pid := startUnrecordedQEMU(t, stateDir, name)
	r, err := loadRec(vmDir)
	if err != nil {
		t.Fatal(err)
	}
	r.PID = pid
	if utils.VerifyProcessCmdline(pid, qemuBinary, r.Disk) {
		t.Fatal("fake QEMU unexpectedly contains the unescaped disk path")
	}
	if !isRunning(r) {
		t.Fatal("live QEMU was not identified")
	}
	cmd := newLifecycleTestCommand(t, stateDir)
	cmd.Flags().Bool("force", true, "")
	if err := Stop(cmd, []string{name}); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if utils.VerifyProcessCmdline(pid, qemuBinary, qemuPIDPath(r.Disk)) {
		t.Error("QEMU survived Stop")
	}
}

func startUnrecordedQEMU(t *testing.T, stateDir, name string) (string, int) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("process cmdline adoption requires /proc")
	}
	cmd := newLifecycleTestCommand(t, stateDir)
	vmDir, err := home.VMDir(cmd, name)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(vmDir, 0o755); err != nil {
		t.Fatal(err)
	}
	disk := filepath.Join(vmDir, "disk.qcow2")
	pid := spawnFakeQEMU(t, disk)
	if err := saveRec(vmDir, &record{Name: name, Disk: disk, VNCDisp: -1}); err != nil {
		t.Fatal(err)
	}
	return vmDir, pid
}

func spawnFakeQEMU(t *testing.T, disk string) int {
	t.Helper()
	fakeQEMU := filepath.Join(t.TempDir(), qemuBinary)
	if err := os.Symlink("/bin/sh", fakeQEMU); err != nil {
		t.Fatal(err)
	}
	pidfile := qemuPIDPath(disk)
	spec := qemu.Spec{Disk: disk, CPUs: 2, Memory: "2048", VNCDisp: -1}
	args := append([]string{"-c", "while :; do sleep 1; done", "qemu"}, spec.Args()...)
	args = append(args, "-daemonize", "-pidfile", pidfile)
	process := exec.Command(fakeQEMU, args...)
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	waitDone := make(chan struct{})
	go func() {
		_ = process.Wait()
		close(waitDone)
	}()
	t.Cleanup(func() {
		_ = process.Process.Kill()
		<-waitDone
	})
	if err := utils.WaitFor(t.Context(), 5*time.Second, time.Millisecond, func() (bool, error) {
		return utils.VerifyProcessCmdline(process.Process.Pid, qemuBinary, pidfile), nil
	}); err != nil {
		t.Fatalf("fake QEMU did not start: %v", err)
	}
	return process.Process.Pid
}

func newLifecycleTestCommand(t *testing.T, stateDir string) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())
	cmd.Flags().String("state-dir", stateDir, "")
	return cmd
}
