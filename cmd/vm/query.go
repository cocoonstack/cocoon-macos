package vm

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/cocoonstack/cocoon-macos/internal/home"
)

// List prints every VM's record as a JSON array.
func (h *Handler) List(cmd *cobra.Command, _ []string) error {
	vmsDir := filepath.Join(home.Dir(cmd), "vms")
	ents, _ := os.ReadDir(vmsDir)
	recs := []*record{}
	for _, e := range ents {
		if r, err := loadRec(filepath.Join(vmsDir, e.Name())); err == nil {
			recs = append(recs, r)
		}
	}
	b, _ := json.MarshalIndent(recs, "", "  ")
	fmt.Println(string(b))
	return nil
}

// Inspect prints a single VM's record as JSON.
func (h *Handler) Inspect(cmd *cobra.Command, args []string) error {
	r, err := loadRec(vmDir(cmd, args[0]))
	if err != nil {
		return err
	}
	b, _ := json.MarshalIndent(r, "", "  ")
	fmt.Println(string(b))
	return nil
}

// Console prints the VNC display and SSH command for reaching a VM.
func (h *Handler) Console(cmd *cobra.Command, args []string) error {
	r, err := loadRec(vmDir(cmd, args[0]))
	if err != nil {
		return err
	}
	fmt.Printf("VNC 127.0.0.1:590%d   SSH: ssh -p %d cocoon@localhost\n", r.VNCDisp, r.SSHPort)
	return nil
}
