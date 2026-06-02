package firmware

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

// Handler installs/lists the shared loader+firmware under <state-dir>/firmware. OVMF_CODE is used
// read-only by every VM; OpenCore + OVMF_VARS are the base/template per-VM copies derive from.
type Handler struct{}

func NewHandler() *Handler { return &Handler{} }

// asset maps an install flag to its managed filename.
type asset struct{ flag, name string }

var assets = []asset{
	{"opencore", "OpenCore.qcow2"},
	{"ovmf-code", "OVMF_CODE.fd"},
	{"ovmf-vars", "OVMF_VARS.fd"},
}

func stateDir(cmd *cobra.Command) string {
	if d, _ := cmd.Flags().GetString("state-dir"); d != "" {
		return d
	}
	if d := os.Getenv("COCOON_MACOS_HOME"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cocoon-macos")
}

func dir(cmd *cobra.Command) string { return filepath.Join(stateDir(cmd), "firmware") }

func copyInto(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open %s: %w", src, err)
	}
	defer func() { _ = in.Close() }()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}

func (h *Handler) Install(cmd *cobra.Command, args []string) error {
	installed := 0
	for _, a := range assets {
		src, _ := cmd.Flags().GetString(a.flag)
		if src == "" {
			continue
		}
		dst := filepath.Join(dir(cmd), a.name)
		if err := copyInto(src, dst); err != nil {
			return err
		}
		fmt.Printf("installed %s -> %s\n", src, dst)
		installed++
	}
	if installed == 0 {
		return fmt.Errorf("nothing to install: pass --opencore and/or --ovmf-code and/or --ovmf-vars")
	}
	return nil
}

func (h *Handler) List(cmd *cobra.Command, args []string) error {
	d := dir(cmd)
	fmt.Println(d + ":")
	for _, n := range []string{"OpenCore.qcow2", "OVMF_CODE.fd", "OVMF_VARS.fd", "CLOUDHV.fd"} {
		if fi, err := os.Stat(filepath.Join(d, n)); err == nil {
			fmt.Printf("  %-16s %d bytes\n", n, fi.Size())
		} else {
			fmt.Printf("  %-16s (absent)\n", n)
		}
	}
	return nil
}
