package vm

import (
	"time"

	"github.com/cocoonstack/cocoon-macos/qemu"
)

const (
	qemuBinary      = "qemu-system-x86_64"
	stopGracePeriod = 10 * time.Second

	// --net modes, needed cross-platform for flag validation; provisioning is Linux-only.
	netUser   = "user"
	netTAP    = "tap"
	netBridge = "bridge"
	netCNI    = "cni"
)

var _ Actions = (*Handler)(nil)

// Handler implements the vm Actions: per-VM CoW overlays on a golden macOS qcow2, booted by
// qemu-system-x86_64 on an x86 Linux/KVM host.
type Handler struct{}

// NewHandler returns a Handler ready to serve the vm subcommands.
func NewHandler() *Handler { return &Handler{} }

// record is the persisted per-VM state, stored as <state-dir>/vms/<name>/vm.json.
type record struct {
	Name        string `json:"name"`
	VMID        string `json:"vmid,omitempty"` // random network-plane id (cocoon VMIDPrefix); != Name
	Image       string `json:"image"`
	ImageDigest string `json:"image_digest,omitempty"`

	CPUs      int      `json:"cpus"`
	Memory    string   `json:"memory"`
	VNCDisp   int      `json:"vnc"`
	VNCPass   string   `json:"-"` // launch-scoped, set from the flag each start; never persisted (would leak at rest)
	SSHPort   int      `json:"ssh_port"`
	NetMode   string   `json:"net_mode,omitempty"`
	Hugepages bool     `json:"hugepages,omitempty"`
	DataDisks []string `json:"data_disks,omitempty"` // created data-disk qcow2 paths, attached on AHCI ports 0,1,3,5

	Disk         string       `json:"disk"`
	OpenCore     string       `json:"opencore"`
	OpenCoreBase string       `json:"opencore_base,omitempty"` // base the OpenCore overlay rests on (clone overlays here); "" when OpenCore is itself the base
	OVMFCode     string       `json:"ovmf_code"`
	OVMFVars     string       `json:"ovmf_vars"`
	MAC          string       `json:"mac,omitempty"`
	SMBIOS       *qemu.SMBIOS `json:"smbios,omitempty"`
	Tap          string       `json:"tap,omitempty"`
	TapOwned     bool         `json:"tap_owned,omitempty"`  // cocoon auto-created the TAP (tear down on rm); false for user --tap
	BridgeDev    string       `json:"bridge_dev,omitempty"` // bridge to enslave the TAP to; persisted so rm can tear down without --bridge
	Netns        string       `json:"netns,omitempty"`      // netns path the qemu process runs in (CNI); "" otherwise

	PID int `json:"pid"`

	Snapshots []string `json:"snapshots,omitempty"` // qcow2-internal snapshot tags, newest last

	Created string `json:"created"`
}
