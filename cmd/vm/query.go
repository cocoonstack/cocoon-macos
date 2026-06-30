package vm

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/cocoonstack/cocoon-macos/internal/cli"
	"github.com/cocoonstack/cocoon-macos/internal/home"
)

// List renders every VM as a table (NAME STATE CPU MEM NET VNC SSH IMAGE CREATED), or JSON with -o json.
func (h *Handler) List(cmd *cobra.Command, _ []string) error {
	vmsDir := home.VMsDir(cmd)
	ents, _ := os.ReadDir(vmsDir)
	recs := []*record{}
	for _, e := range ents {
		if r, err := loadRec(filepath.Join(vmsDir, e.Name())); err == nil {
			recs = append(recs, r)
		}
	}
	return cli.OutputFormatted(cmd, recs, func(w *tabwriter.Writer) {
		fmt.Fprintln(w, "NAME\tSTATE\tCPU\tMEM\tNET\tVNC\tSSH\tIMAGE\tCREATED") //nolint:errcheck
		for _, r := range recs {
			fmt.Fprintf(w, "%s\t%s\t%d\t%sM\t%s\t%s\t%s\t%s\t%s\n", //nolint:errcheck
				r.Name, vmState(r), r.CPUs, r.Memory, netCol(r),
				vncCol(r), sshCol(r), r.Image, cli.FormatTime(r.Created))
		}
	})
}

// Inspect prints a single VM's full record as JSON.
func (h *Handler) Inspect(cmd *cobra.Command, args []string) error {
	r, err := loadRec(home.VMDir(cmd, args[0]))
	if err != nil {
		return err
	}
	return cli.OutputJSON(r)
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

func vmState(r *record) string {
	if isRunning(r) {
		return "running"
	}
	return "stopped"
}

func netCol(r *record) string {
	if r.NetMode == "" {
		return "user"
	}
	return r.NetMode
}

func vncCol(r *record) string {
	if r.VNCDisp < 0 {
		return "-"
	}
	return strconv.Itoa(5900 + r.VNCDisp)
}

func sshCol(r *record) string {
	if r.SSHPort <= 0 {
		return "-"
	}
	return strconv.Itoa(r.SSHPort)
}
