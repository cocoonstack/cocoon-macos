package image

import (
	"testing"

	"github.com/spf13/cobra"
)

func TestListEmptyStore(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.Flags().String("state-dir", t.TempDir(), "")
	cmd.SetContext(t.Context())
	if err := List(cmd, nil); err != nil {
		t.Fatalf("List on empty store: %v", err)
	}
}

func TestRMUnknownRefIsANoOp(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.Flags().String("state-dir", t.TempDir(), "")
	cmd.SetContext(t.Context())
	if err := RM(cmd, []string{"ghcr.io/cocoonstack/cocoon-macos/taho:26"}); err != nil {
		t.Fatalf("RM of an unknown ref must be a no-op: %v", err)
	}
}
