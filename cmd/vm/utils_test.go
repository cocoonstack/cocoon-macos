package vm

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

func TestStorageFromFlag(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		want    int64
		wantErr bool
	}{
		{name: "unset keeps image size"},
		{name: "docker gigabytes", value: "100G", want: 100 << 30},
		{name: "kubernetes gibibytes", value: "100Gi", want: 100 << 30},
		{name: "explicit gibibytes", value: "100GiB", want: 100 << 30},
		{name: "surrounding whitespace", value: " 100Gi ", want: 100 << 30},
		{name: "plain bytes", value: "107374182400", want: 100 << 30},
		{name: "zero rejected", value: "0", wantErr: true},
		{name: "invalid rejected", value: "large", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := &cobra.Command{}
			cmd.Flags().String("storage", "", "")
			if tt.value != "" {
				if err := cmd.Flags().Set("storage", tt.value); err != nil {
					t.Fatal(err)
				}
			}
			got, err := storageFromFlag(cmd)
			if (err != nil) != tt.wantErr {
				t.Fatalf("storageFromFlag() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("storageFromFlag() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestWithVMLockSurvivesVMDirRemoval(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "vms", "demo")
	acquired := make(chan struct{})
	release := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		if err := withVMLock(t.Context(), dir, func() error {
			close(acquired)
			<-release
			return nil
		}); err != nil {
			t.Errorf("first lock: %v", err)
		}
	})
	<-acquired
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	second := make(chan struct{})
	wg.Go(func() {
		if err := withVMLock(t.Context(), dir, func() error {
			close(second)
			return nil
		}); err != nil {
			t.Errorf("second lock: %v", err)
		}
	})
	select {
	case <-second:
		t.Fatal("second operation acquired while first still held the VM lock")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	wg.Wait()
}

func TestResetIncompleteVMDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "vms", "demo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "OpenCore.qcow2"), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := resetIncompleteVMDir(t.Context(), dir); err != nil {
		t.Fatalf("reset incomplete dir: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("incomplete directory still exists: %v", err)
	}
}

func TestResetIncompleteVMDirRefusesCommittedRecord(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "vms", "demo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "vm.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := resetIncompleteVMDir(t.Context(), dir); err == nil {
		t.Fatal("committed VM directory was accepted as incomplete")
	}
}

func TestGraceFromFlags(t *testing.T) {
	tests := []struct {
		name  string
		force bool
		want  time.Duration
	}{
		{name: "force is immediate", force: true, want: 0},
		{name: "default waits the grace window", force: false, want: stopGracePeriod},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := &cobra.Command{}
			cmd.Flags().Bool("force", false, "")
			if tt.force {
				if err := cmd.Flags().Set("force", "true"); err != nil {
					t.Fatalf("set force: %v", err)
				}
			}
			if got := graceFromFlags(cmd); got != tt.want {
				t.Errorf("graceFromFlags(force=%v): got %v, want %v", tt.force, got, tt.want)
			}
		})
	}
}

func TestVMDirPrefixSeparatesSiblingNames(t *testing.T) {
	needle := vmDirPrefix("/state/vms/foo")
	if !strings.Contains("qemu\x00-drive\x00file=/state/vms/foo/disk.qcow2", needle) {
		t.Fatal("own disk path not matched")
	}
	if strings.Contains("qemu\x00-drive\x00file=/state/vms/foo-clone/disk.qcow2", needle) {
		t.Fatal("sibling foo-clone matched")
	}
}

func TestReadUntilWaitsForTheFullPrompt(t *testing.T) {
	out, ok := hmpTranscript(" set_pass", "word vnc (qemu)\r\n", "Error: No VNC display is present\r\n", "(qemu) ")
	if !ok || !hmpReplied(out) {
		t.Fatalf("readUntil = (%q, %v), want the rejection after the echoed prompt-shaped password", out, ok)
	}
	if out, ok := hmpTranscript(" set_password vnc abcd\r\n"); ok {
		t.Errorf("readUntil = (%q, true), want false when the monitor closes before the prompt", out)
	}
}

func TestHMPRepliedFlagsAnyMessage(t *testing.T) {
	for _, tc := range []struct {
		name string
		out  string
		want bool
	}{
		{"echo and prompt only", " set_password vnc abcd\r\n(qemu) ", false},
		{"invalid parameter", " set_password vnc ab cd\r\nError: invalid parameter value: cd\r\n(qemu) ", true},
		{"display inactive", " set_password vnc abcd\r\nCould not set password\r\n(qemu) ", true},
		{"unterminated quote", " set_password vnc \"abc\r\nset_password: string expected\r\nTry \"help set_password\" for more information\r\n(qemu) ", true},
	} {
		if got := hmpReplied(tc.out); got != tc.want {
			t.Errorf("%s: hmpReplied = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestWithVMLockUnlocksAfterCallerCancel(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "vms", "demo")
	ctx, cancel := context.WithCancel(t.Context())
	if err := withVMLock(ctx, dir, func() error {
		cancel()
		return nil
	}); err != nil {
		t.Fatalf("first lock: %v", err)
	}
	relocked := make(chan error, 1)
	go func() {
		relocked <- withVMLock(t.Context(), dir, func() error { return nil })
	}()
	select {
	case err := <-relocked:
		if err != nil {
			t.Fatalf("second lock: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("second lock blocked: the cancelled caller context suppressed Unlock")
	}
}

func hmpTranscript(chunks ...string) (string, bool) {
	client, monitor := net.Pipe()
	go func() {
		for _, chunk := range chunks {
			_, _ = monitor.Write([]byte(chunk))
		}
		_ = monitor.Close()
	}()
	return readUntil(client, hmpPrompt)
}
