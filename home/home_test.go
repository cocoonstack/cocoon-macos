package home

import (
	"testing"

	"github.com/spf13/cobra"
)

func TestOpenStoreEmpty(t *testing.T) {
	cmd := newTestCmd(t)
	ctx, store, err := OpenStore(cmd)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	imgs, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(imgs) != 0 {
		t.Errorf("images = %d, want 0", len(imgs))
	}
}

func newTestCmd(t *testing.T) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{}
	cmd.Flags().String("state-dir", t.TempDir(), "")
	cmd.SetContext(t.Context())
	return cmd
}
