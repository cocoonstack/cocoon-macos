package image

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/cocoonstack/cocoon/config"
	"github.com/cocoonstack/cocoon/images/cloudimg"
	"github.com/cocoonstack/cocoon/progress"
)

// Handler implements Actions on cocoon's cloudimg store (content-addressed qcow2 blobs under
// <state-dir>/cloudimg). The macOS golden disk is a single immutable qcow2, which is exactly the
// cloudimg shape; vm clone bakes a CoW overlay on the resolved blob (see cmd/vm).
type Handler struct{}

func NewHandler() *Handler { return &Handler{} }

func stateDir(cmd *cobra.Command) string {
	if d, _ := cmd.Flags().GetString("state-dir"); d != "" {
		return d
	}
	if d := os.Getenv("COCOON_MACOS_HOME"); d != "" {
		return d
	}
	return "/var/lib/cocoon-macos" // mirrors cocoon's /var/lib/cocoon
}

func (h *Handler) store(cmd *cobra.Command) (context.Context, *cloudimg.CloudImg, error) {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	s, err := cloudimg.New(ctx, &config.Config{RootDir: stateDir(cmd), DNS: "8.8.8.8,1.1.1.1"})
	return ctx, s, err
}

func isURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

func (h *Handler) Pull(cmd *cobra.Command, args []string) error {
	ctx, s, err := h.store(cmd)
	if err != nil {
		return err
	}
	ref := args[0]
	force, _ := cmd.Flags().GetBool("force")
	if isURL(ref) {
		return s.Pull(ctx, ref, force, progress.Nop)
	}
	// OCI/ghcr ref: oras-pull the qcow2 artifact, then import the disk into the store.
	tmp, err := os.MkdirTemp("", "img-pull-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	if out, e := exec.CommandContext(ctx, "oras", "pull", ref, "-o", tmp).CombinedOutput(); e != nil {
		return fmt.Errorf("oras pull %s: %v: %s", ref, e, out)
	}
	q, err := findQcow2(tmp)
	if err != nil {
		return err
	}
	f, err := os.Open(q)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return s.ImportFromReader(ctx, ref, progress.Nop, f)
}

func findQcow2(dir string) (string, error) {
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".qcow2") {
			return filepath.Join(dir, e.Name()), nil
		}
	}
	return "", fmt.Errorf("no .qcow2 in pulled artifact at %s", dir)
}

func (h *Handler) List(cmd *cobra.Command, args []string) error {
	ctx, s, err := h.store(cmd)
	if err != nil {
		return err
	}
	imgs, err := s.List(ctx)
	if err != nil {
		return err
	}
	if len(imgs) == 0 {
		fmt.Println("[]") // an empty store is [], not null
		return nil
	}
	b, _ := json.MarshalIndent(imgs, "", "  ")
	fmt.Println(string(b))
	return nil
}

func (h *Handler) Inspect(cmd *cobra.Command, args []string) error {
	ctx, s, err := h.store(cmd)
	if err != nil {
		return err
	}
	img, err := s.Inspect(ctx, args[0])
	if err != nil {
		return err
	}
	if img == nil {
		return fmt.Errorf("image not found: %s", args[0])
	}
	b, _ := json.MarshalIndent(img, "", "  ")
	fmt.Println(string(b))
	return nil
}

func (h *Handler) RM(cmd *cobra.Command, args []string) error {
	ctx, s, err := h.store(cmd)
	if err != nil {
		return err
	}
	deleted, err := s.Delete(ctx, args)
	if err != nil {
		return err
	}
	for _, d := range deleted {
		fmt.Println(d)
	}
	return nil
}
