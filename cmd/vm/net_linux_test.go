//go:build linux

package vm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/cocoonstack/cocoon-macos/home"
)

func TestNetConfResolvesFlagThenRecordThenDefault(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.Flags().String("cni-conf-dir", "", "")
	cmd.Flags().String("cni-bin-dir", "", "")
	if got := netConf(cmd, &record{}).CNIConfDir; got != "/etc/cni/net.d" {
		t.Errorf("default CNIConfDir = %q", got)
	}
	if got := netConf(cmd, &record{CNIConfDir: "/rec/net.d", CNIBinDir: "/rec/bin"}).CNIBinDir; got != "/rec/bin" {
		t.Errorf("record CNIBinDir = %q", got)
	}
	if err := cmd.Flags().Set("cni-conf-dir", "/flag/net.d"); err != nil {
		t.Fatal(err)
	}
	if got := netConf(cmd, &record{CNIConfDir: "/rec/net.d"}).CNIConfDir; got != "/flag/net.d" {
		t.Errorf("flag CNIConfDir = %q, want the flag over the record", got)
	}
}

func TestNetConfScope(t *testing.T) {
	conf := netConf(&cobra.Command{}, &record{})
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

func TestRMRetainsStateWhenNetworkTeardownFails(t *testing.T) {
	stateDir := t.TempDir()
	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())
	cmd.Flags().String("state-dir", stateDir, "")
	cmd.Flags().Bool("force", false, "")
	dir, err := home.VMDir(cmd, "macos-demo")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	r := &record{
		Name: "macos-demo", VMID: "TESTVMID", Disk: filepath.Join(dir, "disk.qcow2"),
		NetMode: "invalid", TapOwned: true,
	}
	if err := saveRec(dir, r); err != nil {
		t.Fatal(err)
	}

	err = NewHandler().RM(cmd, []string{"macos-demo"})
	if err == nil || !strings.Contains(err.Error(), "unknown --net mode") {
		t.Fatalf("RM error = %v, want network teardown failure", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "vm.json")); err != nil {
		t.Errorf("VM state was removed after teardown failure: %v", err)
	}
}
