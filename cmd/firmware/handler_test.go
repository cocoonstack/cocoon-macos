package firmware

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
)

func TestInstall(t *testing.T) {
	tests := []struct {
		name     string
		opencore bool // pass --opencore with a real source file
		wantErr  bool
	}{
		{"installs the opencore asset", true, false},
		{"errors when nothing to install", false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sd := t.TempDir()
			cmd := &cobra.Command{}
			cmd.Flags().String("state-dir", sd, "")
			cmd.Flags().String("opencore", "", "")
			cmd.Flags().String("ovmf-code", "", "")
			cmd.Flags().String("ovmf-vars", "", "")
			cmd.SetContext(t.Context())
			if tt.opencore {
				src := filepath.Join(sd, "src-oc.qcow2")
				if err := os.WriteFile(src, []byte("fake-oc"), 0o644); err != nil {
					t.Fatalf("setup: %v", err)
				}
				_ = cmd.Flags().Set("opencore", src)
			}

			err := NewHandler().Install(cmd, nil)
			if tt.wantErr {
				if err == nil {
					t.Fatal("got nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("install: %v", err)
			}
			got, rerr := os.ReadFile(filepath.Join(sd, "firmware", "OpenCore.qcow2"))
			if rerr != nil || string(got) != "fake-oc" {
				t.Fatalf("installed OpenCore: got=%q err=%v", got, rerr)
			}
		})
	}
}

func TestList(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.Flags().String("state-dir", t.TempDir(), "")
	cmd.SetContext(t.Context())
	if err := NewHandler().List(cmd, nil); err != nil {
		t.Fatalf("list: %v", err)
	}
}
