package version

import (
	"fmt"
	"runtime"
)

// Build metadata, injected at release time via -ldflags (see Makefile / .goreleaser.yml).
var (
	VERSION  = "unknown"
	REVISION = "HEAD"
	BUILTAT  = "now"
)

// String returns a multi-line version information summary.
func String() string {
	return fmt.Sprintf(
		"Version:        %s\nGit hash:       %s\nBuilt:          %s\nGolang version: %s\nOS/Arch:        %s/%s\n",
		VERSION, REVISION, BUILTAT, runtime.Version(), runtime.GOOS, runtime.GOARCH,
	)
}
