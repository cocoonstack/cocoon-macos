package image

import (
	"testing"

	"github.com/spf13/cobra"
)

func TestListEmptyStore(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.Flags().String("state-dir", t.TempDir(), "")
	cmd.SetContext(t.Context())
	if err := NewHandler().List(cmd, nil); err != nil {
		t.Fatalf("List on empty store: %v", err)
	}
}
