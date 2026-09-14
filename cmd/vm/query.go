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

type vmOutput struct {
	*record
	State string `json:"state"`
}

func (h *Handler) List(cmd *cobra.Command, _ []string) error {
	vmsDir := home.VMsDir(cmd)
	names, err := utils.ScanSubdirs(vmsDir)
	if err != nil {
		return err
	}
	vms := []vmOutput{}
	for _, n := range names {
		if r, err := loadRec(filepath.Join(vmsDir, n)); err == nil {
			vms = append(vms, vmOutput{r, vmState(r)})
		}
	}
	return cliutil.OutputFormatted(cmd, vms, func(w *tabwriter.Writer) {
		fmt.Fprintln(w, "NAME\tSTATE\tCPU\tMEM\tNET\tVNC\tSSH\tIMAGE\tCREATED") //nolint:errcheck // the tabwriter flush reports the write error
		for _, v := range vms {
			fmt.Fprintf(w, "%s\t%s\t%d\t%sM\t%s\t%s\t%s\t%s\t%s\n", //nolint:errcheck // the tabwriter flush reports the write error
				v.Name, v.State, v.CPUs, v.Memory, cmp.Or(v.NetMode, netUser),
				vncCol(v.record), sshCol(v.record), v.Image, formatTime(v.Created))
		}
	})
}

func (h *Handler) Inspect(cmd *cobra.Command, args []string) error {
	dir, err := home.VMDir(cmd, args[0])
	if err != nil {
		return err
	}
	r, err := loadRec(dir)
	if err != nil {
		return err
	}
	return cliutil.OutputJSON(vmOutput{r, vmState(r)})
}

func (h *Handler) Console(cmd *cobra.Command, args []string) error {
	dir, err := home.VMDir(cmd, args[0])
	if err != nil {
		return err
	}
	r, err := loadRec(dir)
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
