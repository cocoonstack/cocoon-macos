package vm

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/cocoonstack/cocoon/utils"
)

func TestRequireCNIVNCPassword(t *testing.T) {
	tests := []struct {
		name    string
		isCNI   bool
		vncDisp int
		vncPass string
		wantErr bool
	}{
		{"cni vnc no password rejected", true, 0, "", true},
		{"cni vnc with password ok", true, 0, "s3cret", false},
		{"cni vnc disabled ok", true, -1, "", false},
		{"non-cni vnc no password ok", false, 0, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := requireCNIVNCPassword(tt.isCNI, tt.vncDisp, tt.vncPass)
			if tt.wantErr && !errors.Is(err, errCNIVNCPassRequired) {
				t.Errorf("got %v, want errCNIVNCPassRequired", err)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("got %v, want nil", err)
			}
		})
	}
}

func TestValidateVNCPassword(t *testing.T) {
	tests := []struct {
		name    string
		pw      string
		wantErr bool
	}{
		{"empty ok", "", false},
		{"normal ok", "s3cret", false},
		{"max length ok", "8charpwd", false},
		{"too long rejected", "9charpass", true},
		{"newline injection rejected", "x\nquit", true},
		{"carriage return rejected", "x\rset", true},
		{"tab rejected", "a\tb", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateVNCPassword(tt.pw); (err != nil) != tt.wantErr {
				t.Errorf("validateVNCPassword(%q) err = %v, wantErr %v", tt.pw, err, tt.wantErr)
			}
		})
	}
}

func TestVNCProxyRunning(t *testing.T) {
	dir := t.TempDir()
	if vncProxyRunning(dir) {
		t.Fatal("missing proxy pidfile reported as running")
	}

	sock := filepath.Join(dir, vncSockName)
	proxy := exec.Command(os.Args[0], "-test.run=TestVNCProxyHelperProcess", "--", vncProxyOp, sock)
	proxy.Env = append(os.Environ(), "COCOON_MACOS_VNC_PROXY_HELPER=1")
	if err := proxy.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = proxy.Process.Kill()
		_ = proxy.Wait()
	})
	if err := utils.WritePIDFile(filepath.Join(dir, vncProxyPID), proxy.Process.Pid); err != nil {
		t.Fatal(err)
	}

	if err := utils.WaitFor(t.Context(), 2*time.Second, 10*time.Millisecond, func() (bool, error) {
		return vncProxyRunning(dir), nil
	}); err != nil {
		t.Fatalf("matching proxy process reported as stopped: %v", err)
	}
}

func TestVNCProxyHelperProcess(t *testing.T) {
	if os.Getenv("COCOON_MACOS_VNC_PROXY_HELPER") != "1" {
		return
	}
	time.Sleep(time.Minute)
	os.Exit(0)
}
