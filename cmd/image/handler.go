package image

import (
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/projecteru2/core/log"
	"github.com/spf13/cobra"

	"github.com/cocoonstack/cocoon/cmd/cliutil"
	"github.com/cocoonstack/cocoon/progress"

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
// has its qcow2 layer streamed off the registry (native oras-go) and imported.
func (h *Handler) Pull(cmd *cobra.Command, args []string) error {
	ctx, s, err := home.OpenStore(cmd)
	if err != nil {
		return err
	}
	ref := args[0]
	force, _ := cmd.Flags().GetBool("force")
	if cliutil.IsURL(ref) {
		return s.Pull(ctx, ref, force, progress.Nop)
	}
	logger := log.WithFunc("cmd.image.Pull")
	// mirror the URL path's idempotency: skip the multi-GiB registry download when already present
	if !force {
		if img, ierr := s.Inspect(ctx, ref); ierr == nil && img != nil {
			logger.Infof(ctx, "image %s already present (use --force to re-pull)", ref)
			return nil
		}
	}
	// Native OCI transport via oras-go: resolve the manifest, pick the qcow2 layer, and stream the
	// blob straight into the store. Credentials still come from the user's docker config (public
	// images pull anonymously), so no external oras binary is needed.
	logger.Debugf(ctx, "pulling OCI artifact %s via oras-go", ref)
	rc, err := pullOCILayer(ctx, ref)
	if err != nil {
		return err
	}
	defer func() { _ = rc.Close() }()
	return s.ImportFromReader(ctx, ref, progress.Nop, rc)
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
	return cliutil.OutputFormatted(cmd, imgs, func(w *tabwriter.Writer) {
		fmt.Fprintln(w, "NAME\tTYPE\tSIZE\tDIGEST\tCREATED") //nolint:errcheck
		for _, img := range imgs {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", //nolint:errcheck
				img.Name, img.Type, cliutil.FormatSize(img.Size),
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
	return cliutil.OutputJSON(img)
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

func shortDigest(id string) string {
	const maxLen = 19 // "sha256:" + 12 hex chars, the conventional short-digest length
	if len(id) > maxLen {
		return id[:maxLen]
	}
	return id
}
