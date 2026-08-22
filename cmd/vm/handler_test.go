package vm

import (
	"slices"
	"testing"

	"github.com/spf13/cobra"
)

func TestCloneOpenCoreBase(t *testing.T) {
	tests := []struct {
		name string
		src  *record
		want string
	}{
		{"recorded base is inherited, not the source overlay", &record{OpenCore: "/vms/src/OpenCore.qcow2", OpenCoreBase: "/fw/OpenCore.qcow2"}, "/fw/OpenCore.qcow2"},
		{"no identity: OpenCore is itself the base", &record{OpenCore: "/fw/OpenCore.qcow2"}, "/fw/OpenCore.qcow2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := cloneOpenCoreBase(&cobra.Command{}, tt.src)
			if err != nil {
				t.Fatalf("cloneOpenCoreBase: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestImagesToSnapshot(t *testing.T) {
	tests := []struct {
		name string
		rec  *record
		want []string
	}{
		// raw .fd NVRAM can't hold internal snapshots, so only the disk is captured
		{"raw nvram captures disk only", &record{Disk: "/v/disk.qcow2", OVMFVars: "/v/OVMF_VARS.fd"}, []string{"/v/disk.qcow2"}},
		// a qcow2 NVRAM rolls back too
		{"qcow2 nvram captures both", &record{Disk: "/v/disk.qcow2", OVMFVars: "/v/OVMF_VARS.qcow2"}, []string{"/v/disk.qcow2", "/v/OVMF_VARS.qcow2"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := imagesToSnapshot(tt.rec); !slices.Equal(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

// auto-create/CNI/bridge need Linux + CAP_NET_ADMIN; they are smoke-tested on the testbed.
func TestPrepareNetNoProvision(t *testing.T) {
	tests := []struct {
		name                        string
		rec                         *record
		wantTap, wantNetns, wantMAC string
	}{
		{"user-mode", &record{NetMode: "user", MAC: "aa:bb:cc:dd:ee:ff"}, "", "", "aa:bb:cc:dd:ee:ff"},
		{"pre-created tap", &record{NetMode: "tap", Tap: "tap0", MAC: "aa:bb:cc:dd:ee:ff"}, "tap0", "", "aa:bb:cc:dd:ee:ff"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := &cobra.Command{}
			cmd.SetContext(t.Context())
			tap, netns, mac, err := prepareNet(cmd, tt.rec)
			if err != nil {
				t.Fatalf("prepareNet: %v", err)
			}
			if tap != tt.wantTap || netns != tt.wantNetns || mac != tt.wantMAC {
				t.Errorf("got tap=%q netns=%q mac=%q; want %q %q %q", tap, netns, mac, tt.wantTap, tt.wantNetns, tt.wantMAC)
			}
		})
	}
}
