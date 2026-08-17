//go:build linux

package vm

import (
	"testing"

	"github.com/spf13/cobra"
)

func TestNetConfScope(t *testing.T) {
	conf := netConf(&cobra.Command{})
	if got, want := conf.NetScope, "cm"; got != want {
		t.Errorf("NetScope = %q, want %q", got, want)
	}
	if got, want := conf.BridgeTAPPrefix(), "cm"; got != want {
		t.Errorf("BridgeTAPPrefix() = %q, want %q", got, want)
	}
	if got, want := conf.NetnsPrefix(), "cm-"; got != want {
		t.Errorf("NetnsPrefix() = %q, want %q", got, want)
	}
}
