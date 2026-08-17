package vm

import (
	"testing"

	"github.com/spf13/cobra"
)

func TestExportRestartVNCPreservesCurrentDisplay(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.Flags().Int("vnc", -1, "")
	cmd.Flags().String("vnc-password", "", "")

	vnc, password := exportRestartVNC(cmd, &record{VNCDisp: 3, VNCPass: "secret"})
	if vnc != 3 || password != "secret" {
		t.Fatalf("restart VNC = (%d, %q), want (3, secret)", vnc, password)
	}
}

func TestExportRestartVNCUsesExplicitOverrides(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.Flags().Int("vnc", -1, "")
	cmd.Flags().String("vnc-password", "", "")
	if err := cmd.Flags().Set("vnc", "1"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Flags().Set("vnc-password", "newpass"); err != nil {
		t.Fatal(err)
	}

	vnc, password := exportRestartVNC(cmd, &record{VNCDisp: 3, VNCPass: "oldpass"})
	if vnc != 1 || password != "newpass" {
		t.Fatalf("restart VNC = (%d, %q), want (1, newpass)", vnc, password)
	}
}
