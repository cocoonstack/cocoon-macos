package firmware

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
)

func TestInstallAndList(t *testing.T) {
	sd := t.TempDir()
	src := filepath.Join(sd, "src-oc.qcow2")
	if err := os.WriteFile(src, []byte("fake-oc"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := &cobra.Command{}
	cmd.Flags().String("state-dir", sd, "")
	cmd.Flags().String("opencore", src, "")
	cmd.Flags().String("ovmf-code", "", "")
	cmd.Flags().String("ovmf-vars", "", "")
	cmd.SetContext(t.Context())
	if err := NewHandler().Install(cmd, nil); err != nil {
		t.Fatalf("install: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(sd, "firmware", "OpenCore.qcow2"))
	if err != nil || string(got) != "fake-oc" {
		t.Fatalf("installed OpenCore: got=%q err=%v", got, err)
	}
	if err := NewHandler().List(cmd, nil); err != nil {
		t.Fatalf("list: %v", err)
	}
}

func TestInstallNothing(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.Flags().String("state-dir", t.TempDir(), "")
	cmd.Flags().String("opencore", "", "")
	cmd.Flags().String("ovmf-code", "", "")
	cmd.Flags().String("ovmf-vars", "", "")
	cmd.SetContext(t.Context())
	if err := NewHandler().Install(cmd, nil); err == nil {
		t.Fatal("expected error when nothing to install")
	}
}
