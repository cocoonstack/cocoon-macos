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
	vmsDir := home.VMsDir(cmd)
	ents, _ := os.ReadDir(vmsDir)
	recs := []*record{}
	for _, e := range ents {
		if r, err := loadRec(filepath.Join(vmsDir, e.Name())); err == nil {
			recs = append(recs, r)
		}
	}
	printJSON(recs)
	return nil
}

// Inspect prints a single VM's record as JSON.
func (h *Handler) Inspect(cmd *cobra.Command, args []string) error {
	r, err := loadRec(home.VMDir(cmd, args[0]))
	if err != nil {
		return err
	}
	printJSON(r)
	return nil
}

// Console prints the VNC display and SSH command for reaching a VM.
func (h *Handler) Console(cmd *cobra.Command, args []string) error {
	r, err := loadRec(home.VMDir(cmd, args[0]))
	if err != nil {
		return err
	}
	fmt.Printf("VNC 127.0.0.1:590%d   SSH: ssh -p %d cocoon@localhost\n", r.VNCDisp, r.SSHPort)
	return nil
}

func printJSON(v any) {
	b, _ := json.MarshalIndent(v, "", "  ")
	fmt.Println(string(b))
}
