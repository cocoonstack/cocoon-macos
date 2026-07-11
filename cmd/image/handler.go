package image

import (
	"fmt"
	"os"
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
	// ghcr resets a single HTTP/2 stream on multi-GB blobs, so pull to a temp file over parallel
	// Range connections and import that, rather than streaming straight into the store.
	logger.Debugf(ctx, "pulling OCI artifact %s via parallel Range download", ref)
	tmp, err := os.CreateTemp(home.Dir(cmd), "pull-*.qcow2")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	_ = tmp.Close()
	defer func() { _ = os.Remove(tmpPath) }()

	var perr error
	for attempt := 1; attempt <= 3; attempt++ {
		if perr = pullOCIBlob(ctx, ref, tmpPath); perr == nil {
			return s.Import(ctx, ref, progress.Nop, tmpPath)
		}
		logger.Warnf(ctx, "oci pull attempt %d/3 failed: %v", attempt, perr)
		if attempt < 3 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt) * 10 * time.Second):
			}
		}
	}
	return fmt.Errorf("pull %s after 3 attempts: %w", ref, perr)
}

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
