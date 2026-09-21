package vm

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cocoonstack/cocoon/utils"
)

func TestScaffoldRejectsLongSocketPath(t *testing.T) {
	root := filepath.Join(t.TempDir(), strings.Repeat("s", 107))
	cmd := newLifecycleTestCommand(t, root)
	if _, _, _, _, err := scaffoldVM(cmd, "n", "missing", "missing"); err == nil || !strings.Contains(err.Error(), "107-byte") {
		t.Fatalf("scaffold = %v, want a socket path refusal before resolving images", err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("overlong state root was created: %v", err)
	}
}

func TestCreatePinsLocalPaths(t *testing.T) {
	if _, err := exec.LookPath("qemu-img"); err != nil {
		t.Skip("qemu-img is required to create overlays")
	}
	root, err := os.MkdirTemp("", "cm-path")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	t.Chdir(root)
	root, err = os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := utils.RunQemuImg(t.Context(), "create", "-f", "qcow2", "base.qcow2", "4M"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"oc.qcow2", "code.fd", "vars.fd"} {
		if err := os.WriteFile(name, []byte("firmware"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cmd := Command()
	cmd.PersistentFlags().String("state-dir", filepath.Join(root, "state"), "")
	cmd.SetArgs([]string{"create", "base.qcow2", "--name", "n", "--opencore", "oc.qcow2", "--ovmf-code", "code.fd", "--ovmf-vars", "vars.fd"})
	if err := cmd.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	r, err := loadRec(filepath.Join(root, "state", "vms", "n"))
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{
		r.Image:    filepath.Join(root, "base.qcow2"),
		r.OpenCore: filepath.Join(root, "oc.qcow2"),
		r.OVMFCode: filepath.Join(root, "code.fd"),
	} {
		if path != want {
			t.Errorf("recorded path = %q, want %q", path, want)
		}
	}
	t.Chdir("/")
	if err := bakeOverlay(t.Context(), cloneImageBase(cmd, r), filepath.Join(root, "clone.qcow2")); err != nil {
		t.Fatalf("clone base after changing directory: %v", err)
	}
}
