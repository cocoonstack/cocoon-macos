// Package cli holds small CLI-output helpers shared across cocoon-macos subcommands. They mirror
// cocoon's "-o table|json" UX with the standard library, so the surfaces match without importing
// cocoon's heavy cmd/core command package.
package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/docker/go-units"
	"github.com/spf13/cobra"
)

// AddFormatFlag registers the -o/--format flag (table or json) used by list commands.
func AddFormatFlag(cmd *cobra.Command) {
	cmd.Flags().StringP("format", "o", "table", `output format: "table" or "json"`)
}

// OutputFormatted prints data as JSON when --format=json, otherwise renders a padded table via write.
func OutputFormatted(cmd *cobra.Command, data any, write func(w *tabwriter.Writer)) error {
	if format, _ := cmd.Flags().GetString("format"); format == "json" {
		return OutputJSON(data)
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	write(w)
	return w.Flush()
}

// OutputJSON prints v as indented JSON on stdout.
func OutputJSON(v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal json: %w", err)
	}
	fmt.Println(string(b))
	return nil
}

// FormatSize renders a byte count as a human-readable size via the same units.HumanSize cocoon
// uses (e.g. 13.46GB), so sizes match across the two CLIs.
func FormatSize(b int64) string {
	return units.HumanSize(float64(b))
}

// FormatTime renders an RFC3339 timestamp string as local "2006-01-02 15:04:05"; raw on parse failure.
func FormatTime(s string) string {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.Local().Format(time.DateTime)
	}
	return s
}
