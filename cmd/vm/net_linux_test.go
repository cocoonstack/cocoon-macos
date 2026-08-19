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

func TestParseLinkMAC(t *testing.T) {
	mac, err := parseLinkMAC([]byte(`[{"ifindex":2,"ifname":"eth0","address":"AE:77:7B:3B:49:88"}]`))
	if err != nil {
		t.Fatalf("parseLinkMAC: %v", err)
	}
	if want := "ae:77:7b:3b:49:88"; mac != want {
		t.Errorf("MAC = %q, want %q", mac, want)
	}
}
