package image

import (
	"testing"

	"github.com/spf13/cobra"
)

// TestListEmptyStore verifies List succeeds on a fresh state dir without network or qemu-img; pull/import are covered on Linux.
func TestListEmptyStore(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.Flags().String("state-dir", t.TempDir(), "")
	cmd.SetContext(t.Context())
	if err := NewHandler().List(cmd, nil); err != nil {
		t.Fatalf("List on empty store: %v", err)
	}
}
