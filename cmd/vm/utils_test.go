package vm

import (
	"os"
	"path/filepath"
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
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := withVMLock(t.Context(), dir, func() error {
			close(acquired)
			<-release
			return nil
		}); err != nil {
			t.Errorf("first lock: %v", err)
		}
	}()
	<-acquired
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	second := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := withVMLock(t.Context(), dir, func() error {
			close(second)
			return nil
		}); err != nil {
			t.Errorf("second lock: %v", err)
		}
	}()
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
