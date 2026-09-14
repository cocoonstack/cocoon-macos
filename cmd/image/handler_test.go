package image

import (
	"strings"
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

func TestRMUnknownRef(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.Flags().String("state-dir", t.TempDir(), "")
	cmd.SetContext(t.Context())
	err := RM(cmd, []string{"ghcr.io/cocoonstack/cocoon-macos/taho:26"})
	if err == nil || !strings.Contains(err.Error(), "image not found") {
		t.Fatalf("RM error = %v, want the not-found refusal", err)
	}
}
