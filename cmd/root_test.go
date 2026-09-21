package cmd

import (
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
)

func TestResolvePaths(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("COCOON_MACOS_HOME", "environment")
	for _, state := range []string{"", "explicit"} {
		cmd := &cobra.Command{}
		cmd.Flags().String("state-dir", state, "")
		cmd.Flags().String("cni-conf-dir", "config", "")
		cmd.Flags().String("cni-bin-dir", "", "")
		if err := resolvePaths(cmd, nil); err != nil {
			t.Fatal(err)
		}
		if state == "" {
			state = "environment"
		}
		for name, path := range map[string]string{"state-dir": state, "cni-conf-dir": "config"} {
			want, err := filepath.Abs(path)
			if err != nil {
				t.Fatal(err)
			}
			if got, _ := cmd.Flags().GetString(name); got != want {
				t.Errorf("%s = %q, want %q", name, got, want)
			}
		}
		if cmd.Flags().Changed("cni-bin-dir") {
			t.Fatal("unset CNI paths must retain clone inheritance")
		}
	}
}
