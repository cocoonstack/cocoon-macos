package vm

import (
	"testing"

	"github.com/spf13/cobra"
)

func TestImagesToSnapshot(t *testing.T) {
	// raw .fd NVRAM can't hold internal snapshots, so only the disk is captured
	got := imagesToSnapshot(&record{Disk: "/v/disk.qcow2", OVMFVars: "/v/OVMF_VARS.fd"})
	if len(got) != 1 || got[0] != "/v/disk.qcow2" {
		t.Fatalf("raw NVRAM: want [disk], got %v", got)
	}
	// a qcow2 NVRAM rolls back too
	got = imagesToSnapshot(&record{Disk: "/v/disk.qcow2", OVMFVars: "/v/OVMF_VARS.qcow2"})
	if len(got) != 2 || got[1] != "/v/OVMF_VARS.qcow2" {
		t.Fatalf("qcow2 NVRAM: want [disk, nvram], got %v", got)
	}
}

// TestPrepareNetNoProvision covers the paths that need no host networking (identical on every OS,
// so they run in CI regardless of platform): user-mode allocates no TAP, a pre-created --tap is
// returned verbatim. Auto-create / CNI / bridge need Linux + CAP_NET_ADMIN and are smoke-tested
// on the testbed.
func TestPrepareNetNoProvision(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())

	tap, netns, mac, err := prepareNet(cmd, &record{NetMode: "user", MAC: "aa:bb:cc:dd:ee:ff"})
	if err != nil || tap != "" || netns != "" || mac != "aa:bb:cc:dd:ee:ff" {
		t.Fatalf("user-mode: got tap=%q netns=%q mac=%q err=%v", tap, netns, mac, err)
	}

	tap, netns, _, err = prepareNet(cmd, &record{NetMode: "tap", Tap: "tap0", MAC: "aa:bb:cc:dd:ee:ff"})
	if err != nil || tap != "tap0" || netns != "" {
		t.Fatalf("pre-created tap: got tap=%q netns=%q err=%v", tap, netns, err)
	}
}
