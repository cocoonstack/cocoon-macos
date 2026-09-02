package vm

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/cocoonstack/cocoon-macos/home"
	"github.com/cocoonstack/cocoon/utils"
)

func TestSnapshotAdoptsQEMUWhenRecordPIDWasNotCommitted(t *testing.T) {
	stateDir := t.TempDir()
	vmDir, pid := startUnrecordedQEMU(t, stateDir, "macos-demo")
	cmd := newLifecycleTestCommand(t, stateDir)
	cmd.Flags().String("tag", "", "")

	err := NewHandler().Snapshot(cmd, []string{"macos-demo"})
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

func TestRMAdoptsQEMUWhenRecordPIDWasNotCommitted(t *testing.T) {
	stateDir := t.TempDir()
	vmDir, _ := startUnrecordedQEMU(t, stateDir, "macos-demo")
	cmd := newLifecycleTestCommand(t, stateDir)
	cmd.Flags().Bool("force", false, "")
	if err := cmd.Flags().Set("force", "true"); err != nil {
		t.Fatal(err)
	}

	if err := NewHandler().RM(cmd, []string{"macos-demo"}); err != nil {
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

	err = NewHandler().Restore(cmd, []string{"macos-demo"})
	if err == nil || !strings.Contains(err.Error(), "password-gated VNC") {
		t.Fatalf("Restore error = %v, want the password-gated VNC refusal", err)
	}
	if !utils.VerifyProcessCmdline(pid, qemuBinary, filepath.Join(vmDir, "disk.qcow2")) {
		t.Error("qemu was terminated by a refused restore")
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

	err := NewHandler().Clone(cmd, []string{"macos-src"})
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
	fakeQEMU := filepath.Join(t.TempDir(), qemuBinary)
	if err := os.Symlink("/bin/sh", fakeQEMU); err != nil {
		t.Fatal(err)
	}
	disk := filepath.Join(vmDir, "disk.qcow2")
	process := exec.Command(fakeQEMU, "-c", "while :; do sleep 1; done", disk)
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
		return utils.VerifyProcessCmdline(process.Process.Pid, qemuBinary, disk), nil
	}); err != nil {
		t.Fatalf("fake QEMU did not start: %v", err)
	}
	if err := saveRec(vmDir, &record{Name: name, Disk: disk, VNCDisp: -1}); err != nil {
		t.Fatal(err)
	}
	return vmDir, process.Process.Pid
}

func newLifecycleTestCommand(t *testing.T, stateDir string) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())
	cmd.Flags().String("state-dir", stateDir, "")
	return cmd
}
