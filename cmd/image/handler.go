package image

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/projecteru2/core/log"
	"github.com/spf13/cobra"

	"github.com/cocoonstack/cocoon/config"
	"github.com/cocoonstack/cocoon/images/cloudimg"
	"github.com/cocoonstack/cocoon/progress"

	"github.com/cocoonstack/cocoon-macos/internal/home"
)

var _ Actions = (*Handler)(nil)

// Handler implements Actions on cocoon's cloudimg store (content-addressed qcow2 blobs under
// <state-dir>/cloudimg). The macOS golden disk is a single immutable qcow2, which is exactly the
// cloudimg shape; vm clone bakes a CoW overlay on the resolved blob (see cmd/vm).
type Handler struct{}

// NewHandler returns a Handler backed by the cloudimg store.
func NewHandler() *Handler { return &Handler{} }

// Pull fetches IMAGE into the store: an http(s) URL goes straight through cloudimg; an OCI/ghcr ref
// is pulled as a qcow2 artifact and imported.
func (h *Handler) Pull(cmd *cobra.Command, args []string) error {
	ctx, s, err := h.openStore(cmd)
	if err != nil {
		return err
	}
	ref := args[0]
	force, _ := cmd.Flags().GetBool("force")
	if isURL(ref) {
		return s.Pull(ctx, ref, force, progress.Nop)
	}
	// Shell out to the `oras` CLI rather than oras-go: the CLI is the authoritative ghcr transport,
	// reusing the user's docker credentials and the registry token dance transparently, and oras-go is
	// not in the dependency tree. Migrating to the oras-go SDK is tracked as tech debt.
	log.WithFunc("cmd.image.Pull").Debugf(ctx, "pulling OCI artifact %s via the oras CLI", ref)
	tmp, err := os.MkdirTemp("", "img-pull-")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	if out, e := exec.CommandContext(ctx, "oras", "pull", ref, "-o", tmp).CombinedOutput(); e != nil {
		return fmt.Errorf("oras pull %s (output: %s): %w", ref, out, e)
	}
	q, err := findQcow2(tmp)
	if err != nil {
		return err
	}
	f, err := os.Open(q)
	if err != nil {
		return fmt.Errorf("open pulled qcow2: %w", err)
	}
	defer func() { _ = f.Close() }()
	return s.ImportFromReader(ctx, ref, progress.Nop, f)
}

// List prints every stored image as a JSON array ([] when empty, never null).
func (h *Handler) List(cmd *cobra.Command, _ []string) error {
	ctx, s, err := h.openStore(cmd)
	if err != nil {
		return err
	}
	imgs, err := s.List(ctx)
	if err != nil {
		return err
	}
	if len(imgs) == 0 {
		fmt.Println("[]")
		return nil
	}
	printJSON(imgs)
	return nil
}

// Inspect prints a single stored image's metadata as JSON.
func (h *Handler) Inspect(cmd *cobra.Command, args []string) error {
	ctx, s, err := h.openStore(cmd)
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
	printJSON(img)
	return nil
}

// RM deletes one or more images from the store, printing each removed ref.
func (h *Handler) RM(cmd *cobra.Command, args []string) error {
	ctx, s, err := h.openStore(cmd)
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

// openStore opens the cloudimg store rooted at the resolved state dir, returning the command context.
func (h *Handler) openStore(cmd *cobra.Command) (context.Context, *cloudimg.CloudImg, error) {
	ctx := home.Ctx(cmd)
	s, err := cloudimg.New(ctx, &config.Config{RootDir: home.Dir(cmd), DNS: "8.8.8.8,1.1.1.1"})
	if err != nil {
		return ctx, nil, fmt.Errorf("init cloudimg store: %w", err)
	}
	return ctx, s, nil
}

func isURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

func printJSON(v any) {
	b, _ := json.MarshalIndent(v, "", "  ")
	fmt.Println(string(b))
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
