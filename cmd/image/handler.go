package image

import (
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/projecteru2/core/log"
	"github.com/spf13/cobra"

	"github.com/cocoonstack/cocoon-macos/home"
	"github.com/cocoonstack/cocoon/cmd/cliutil"
	"github.com/cocoonstack/cocoon/progress"
)

const maxPullAttempts = 3

// Handler is the image command surface on cocoon's cloudimg store.
type Handler struct{}

// NewHandler returns a Handler backed by the cloudimg store.
func NewHandler() *Handler { return &Handler{} }

func (h *Handler) Pull(cmd *cobra.Command, args []string) error {
	ctx := cliutil.CommandContext(cmd)
	s, err := home.OpenStore(ctx, cmd)
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
	// ghcr resets a single HTTP/2 stream on multi-GB blobs: pull to a temp file over parallel Ranges, then import
	logger.Debugf(ctx, "pulling OCI artifact %s via parallel Range download", ref)
	tmp, err := os.CreateTemp(home.Dir(cmd), "pull-*.qcow2")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	_ = tmp.Close()
	defer func() { _ = os.Remove(tmpPath) }()

	var perr error
	for attempt := 1; attempt <= maxPullAttempts; attempt++ {
		if perr = pullOCIBlob(ctx, ref, tmpPath); perr == nil {
			return s.Import(ctx, ref, progress.Nop, tmpPath)
		}
		logger.Warnf(ctx, "oci pull attempt %d/%d failed: %v", attempt, maxPullAttempts, perr)
		if attempt < maxPullAttempts {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt) * 10 * time.Second):
			}
		}
	}
	return fmt.Errorf("pull %s after %d attempts: %w", ref, maxPullAttempts, perr)
}

func (h *Handler) List(cmd *cobra.Command, _ []string) error {
	ctx := cliutil.CommandContext(cmd)
	s, err := home.OpenStore(ctx, cmd)
	if err != nil {
		return err
	}
	imgs, err := s.List(ctx)
	if err != nil {
		return err
	}
	return cliutil.OutputFormatted(cmd, imgs, func(w *tabwriter.Writer) {
		fmt.Fprintln(w, "NAME\tTYPE\tSIZE\tDIGEST\tCREATED") //nolint:errcheck // the tabwriter flush reports the write error
		for _, img := range imgs {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", //nolint:errcheck // the tabwriter flush reports the write error
				img.Name, img.Type, cliutil.FormatSize(img.Size),
				shortDigest(img.ID), img.CreatedAt.Local().Format(time.DateTime))
		}
	})
}

func (h *Handler) Inspect(cmd *cobra.Command, args []string) error {
	ctx := cliutil.CommandContext(cmd)
	s, err := home.OpenStore(ctx, cmd)
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
	ctx := cliutil.CommandContext(cmd)
	s, err := home.OpenStore(ctx, cmd)
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
