package vm

import (
	"testing"
	"time"

	"github.com/spf13/cobra"
)

func TestGraceFromFlags(t *testing.T) {
	tests := []struct {
		name  string
		force bool
		want  time.Duration
	}{
		{name: "force is immediate", force: true, want: 0},
		{name: "default waits the grace window", force: false, want: stopGracePeriod},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := &cobra.Command{}
			cmd.Flags().Bool("force", false, "")
			if tt.force {
				if err := cmd.Flags().Set("force", "true"); err != nil {
					t.Fatalf("set force: %v", err)
				}
			}
			if got := graceFromFlags(cmd); got != tt.want {
				t.Errorf("graceFromFlags(force=%v): got %v, want %v", tt.force, got, tt.want)
			}
		})
	}
}
