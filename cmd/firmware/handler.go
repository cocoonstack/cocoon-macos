package firmware

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/cocoonstack/cocoon-macos/internal/home"
)

// Handler installs/lists the shared loader+firmware under <state-dir>/firmware. OVMF_CODE is used
// read-only by every VM; OpenCore + OVMF_VARS are the base/template per-VM copies derive from.
type Handler struct{}

var _ Actions = (*Handler)(nil)

// NewHandler returns a Handler that manages the shared firmware store.
func NewHandler() *Handler { return &Handler{} }

// Install copies each firmware asset supplied via flags into the shared store, erroring if no asset
// flag was given.
func (h *Handler) Install(cmd *cobra.Command, _ []string) error {
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

// List prints each managed firmware file under <state-dir>/firmware with its size, or (absent).
func (h *Handler) List(cmd *cobra.Command, _ []string) error {
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

// asset maps an install flag to its managed filename under <state-dir>/firmware.
type asset struct{ flag, name string }

var assets = []asset{
	{"opencore", "OpenCore.qcow2"},
	{"ovmf-code", "OVMF_CODE.fd"},
	{"ovmf-vars", "OVMF_VARS.fd"},
}

func dir(cmd *cobra.Command) string { return filepath.Join(home.Dir(cmd), "firmware") }

func copyInto(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(dst), err)
	}
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open %s: %w", src, err)
	}
	defer func() { _ = in.Close() }()
	out, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("create %s: %w", dst, err)
	}
	defer func() { _ = out.Close() }()
	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("copy %s -> %s: %w", src, dst, err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("close %s: %w", dst, err)
	}
	return nil
}
