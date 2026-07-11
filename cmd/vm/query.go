package vm

import (
	"cmp"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/cocoonstack/cocoon/cmd/cliutil"

	"github.com/cocoonstack/cocoon-macos/home"
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
	return cliutil.OutputFormatted(cmd, recs, func(w *tabwriter.Writer) {
		fmt.Fprintln(w, "NAME\tSTATE\tCPU\tMEM\tNET\tVNC\tSSH\tIMAGE\tCREATED") //nolint:errcheck
		for _, r := range recs {
			fmt.Fprintf(w, "%s\t%s\t%d\t%sM\t%s\t%s\t%s\t%s\t%s\n", //nolint:errcheck
				r.Name, vmState(r), r.CPUs, r.Memory, cmp.Or(r.NetMode, netUser),
				vncCol(r), sshCol(r), r.Image, formatTime(r.Created))
		}
	})
}

// Inspect prints a single VM's full record as JSON.
func (h *Handler) Inspect(cmd *cobra.Command, args []string) error {
	r, err := loadRec(home.VMDir(cmd, args[0]))
	if err != nil {
		return err
	}
	return cliutil.OutputJSON(r)
}

// Console prints the VNC endpoint and SSH command for reaching a VM ("-" when disabled),
// deriving both from the same helpers vm list uses so the 5900+display rule lives in one place.
func (h *Handler) Console(cmd *cobra.Command, args []string) error {
	r, err := loadRec(home.VMDir(cmd, args[0]))
	if err != nil {
		return err
	}
	vnc := vncCol(r)
	if vnc != "-" {
		vnc = "127.0.0.1:" + vnc
	}
	ssh := "-"
	if r.SSHPort > 0 {
		ssh = fmt.Sprintf("ssh -p %d cocoon@localhost", r.SSHPort)
	}
	fmt.Printf("VNC %s   SSH: %s\n", vnc, ssh)
	return nil
}

func vmState(r *record) string {
	if isRunning(r) {
		return "running"
	}
	return "stopped"
}

func vncCol(r *record) string {
	if r.VNCDisp < 0 {
		return "-"
	}
	return strconv.Itoa(vncBasePort + r.VNCDisp)
}

func sshCol(r *record) string {
	if r.SSHPort <= 0 {
		return "-"
	}
	return strconv.Itoa(r.SSHPort)
}

// formatTime renders the record's RFC3339 Created stamp as local time.DateTime; raw on parse failure.
func formatTime(s string) string {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.Local().Format(time.DateTime)
	}
	return s
}
