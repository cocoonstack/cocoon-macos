package image

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/projecteru2/core/log"
	"github.com/spf13/cobra"

	"github.com/cocoonstack/cocoon-macos/home"
	"github.com/cocoonstack/cocoon/images"
	"github.com/cocoonstack/cocoon/images/cloudimg"
	"github.com/cocoonstack/cocoon/progress"
)

func (h *Handler) Import(cmd *cobra.Command, args []string) error {
	ctx, store, err := home.OpenStore(cmd)
	if err != nil {
		return err
	}
	name := args[0]
	if len(args) == 2 {
		if err := store.Import(ctx, name, progress.Nop, args[1]); err != nil {
			return fmt.Errorf("import %s: %w", name, err)
		}
		return nil
	}
	if err := store.ImportFromReader(ctx, name, progress.Nop, os.Stdin); err != nil {
		return fmt.Errorf("import %s from stdin: %w", name, err)
	}
	return nil
}

func (h *Handler) Export(cmd *cobra.Command, args []string) error {
	ctx, store, err := home.OpenStore(cmd)
	if err != nil {
		return err
	}
	ref := args[0]
	image, err := store.Inspect(ctx, ref)
	if err != nil {
		return fmt.Errorf("export %s: %w", ref, err)
	}
	if image == nil {
		return fmt.Errorf("export %s: image not found", ref)
	}
	digestHex := strings.TrimPrefix(image.ID, "sha256:")
	conf := cloudimg.NewConfig(home.Dir(cmd), 0)
	var locks images.BlobLocks
	if lockErr := locks.Lock(conf.BlobLockPath(digestHex)); lockErr != nil {
		return fmt.Errorf("lock cloud image %s: %w", ref, lockErr)
	}
	defer locks.Release()
	stream, err := os.Open(conf.BlobPath(digestHex)) //nolint:gosec
	if err != nil {
		return fmt.Errorf("open cloud image %s: %w", ref, err)
	}
	defer stream.Close() //nolint:errcheck

	output, _ := cmd.Flags().GetString("output")
	if output == "-" {
		if _, err := io.Copy(os.Stdout, stream); err != nil {
			return fmt.Errorf("write cloud image: %w", err)
		}
		return nil
	}
	if output == "" {
		base := strings.ReplaceAll(filepath.Base(ref), ":", "-")
		output = base + ".qcow2"
	}

	log.WithFunc("cmd.image.Export").Infof(ctx, "exporting %s to %s ...", ref, output)
	return writeExportFile(output, stream)
}

// writeExportFile replaces output only after a complete, flushed copy. The
// temporary file lives beside output so rename remains atomic.
func writeExportFile(output string, src io.Reader) (retErr error) {
	dir := filepath.Dir(output)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(output)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create export temp file for %s: %w", output, err)
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = tmp.Close()
		if retErr != nil {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := io.Copy(tmp, src); err != nil {
		return fmt.Errorf("write %s: %w", output, err)
	}
	if err := tmp.Chmod(0o644); err != nil {
		return fmt.Errorf("chmod %s: %w", output, err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync %s: %w", output, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", output, err)
	}
	if err := os.Rename(tmpPath, output); err != nil {
		return fmt.Errorf("replace %s: %w", output, err)
	}
	return nil
}
