package image

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/projecteru2/core/log"
	"github.com/spf13/cobra"

	"github.com/cocoonstack/cocoon/progress"

	"github.com/cocoonstack/cocoon-macos/cli"
	"github.com/cocoonstack/cocoon-macos/home"
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
	ctx, s, err := home.OpenStore(cmd)
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

// List renders stored images as a table (NAME TYPE SIZE DIGEST CREATED), or JSON with -o json.
func (h *Handler) List(cmd *cobra.Command, _ []string) error {
	ctx, s, err := home.OpenStore(cmd)
	if err != nil {
		return err
	}
	imgs, err := s.List(ctx)
	if err != nil {
		return err
	}
	return cli.OutputFormatted(cmd, imgs, func(w *tabwriter.Writer) {
		fmt.Fprintln(w, "NAME\tTYPE\tSIZE\tDIGEST\tCREATED") //nolint:errcheck
		for _, img := range imgs {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", //nolint:errcheck
				img.Name, img.Type, cli.FormatSize(img.Size),
				shortDigest(img.ID), img.CreatedAt.Local().Format(time.DateTime))
		}
	})
}

// Inspect prints a single stored image's metadata as JSON.
func (h *Handler) Inspect(cmd *cobra.Command, args []string) error {
	ctx, s, err := home.OpenStore(cmd)
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
	return cli.OutputJSON(img)
}

// RM deletes one or more images from the store, printing each removed ref.
func (h *Handler) RM(cmd *cobra.Command, args []string) error {
	ctx, s, err := home.OpenStore(cmd)
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

func isURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

func shortDigest(id string) string {
	const maxLen = 19 // "sha256:" + 12 hex chars, the conventional short-digest length
	if len(id) > maxLen {
		return id[:maxLen]
	}
	return id
}

func findQcow2(dir string) (string, error) {
	if matches, _ := filepath.Glob(filepath.Join(dir, "*.qcow2")); len(matches) > 0 {
		return matches[0], nil
	}
	return "", fmt.Errorf("no .qcow2 in pulled artifact at %s", dir)
}
