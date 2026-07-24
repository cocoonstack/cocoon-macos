package vm

import (
	"cmp"
	"fmt"
	"path/filepath"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/cocoonstack/cocoon-macos/home"
	"github.com/cocoonstack/cocoon/cmd/cliutil"
	"github.com/cocoonstack/cocoon/utils"
)

func (h *Handler) List(cmd *cobra.Command, _ []string) error {
	vmsDir := home.VMsDir(cmd)
	names, err := utils.ScanSubdirs(vmsDir)
	if err != nil {
		return err
	}
	recs := []*record{}
	for _, n := range names {
		if r, err := loadRec(filepath.Join(vmsDir, n)); err == nil {
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

func (h *Handler) Inspect(cmd *cobra.Command, args []string) error {
	r, err := loadRec(home.VMDir(cmd, args[0]))
	if err != nil {
		return err
	}
	return cliutil.OutputJSON(r)
}

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

func formatTime(s string) string {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.Local().Format(time.DateTime)
	}
	return s
}
