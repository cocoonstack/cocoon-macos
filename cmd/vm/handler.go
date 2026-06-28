package vm

import (
	"time"

	"github.com/cocoonstack/cocoon-macos/qemu"
)

const (
	qemuBinary      = "qemu-system-x86_64"
	stopGracePeriod = 10 * time.Second
	defaultStateDir = "/var/lib/cocoon-macos" // mirrors cocoon's /var/lib/cocoon; override via --state-dir or $COCOON_MACOS_HOME
)

// Handler implements the vm Actions by cloning a golden macOS qcow2 (copy-on-write overlay)
// and launching qemu-system-x86_64 on an x86 Linux/KVM host.
type Handler struct{}

// NewHandler returns a Handler ready to serve the vm subcommands.
func NewHandler() *Handler { return &Handler{} }

// record is the persisted per-VM state, stored as <state-dir>/vms/<name>/vm.json.
type record struct {
	// identification
	Name        string `json:"name"`
	VMID        string `json:"vmid,omitempty"` // random network-plane id (cocoon VMIDPrefix); != Name
	Image       string `json:"image"`
	ImageDigest string `json:"image_digest,omitempty"`

	// config
	CPUs      int    `json:"cpus"`
	Memory    string `json:"memory"`
	VNCDisp   int    `json:"vnc"`
	VNCPass   string `json:"vnc_password,omitempty"`
	SSHPort   int    `json:"ssh_port"`
	NetMode   string `json:"net_mode,omitempty"`
	Hugepages bool   `json:"hugepages,omitempty"` // back guest RAM with 2 MiB hugepages

	// resources
	Disk      string       `json:"disk"`
	OpenCore  string       `json:"opencore"`
	OVMFCode  string       `json:"ovmf_code"`
	OVMFVars  string       `json:"ovmf_vars"`
	MAC       string       `json:"mac,omitempty"`
	SMBIOS    *qemu.SMBIOS `json:"smbios,omitempty"`
	Tap       string       `json:"tap,omitempty"`
	TapOwned  bool         `json:"tap_owned,omitempty"`  // cocoon auto-created the TAP (tear down on rm); false for user --tap
	BridgeDev string       `json:"bridge_dev,omitempty"` // bridge to enslave the TAP to; persisted so rm can tear down without --bridge
	Netns     string       `json:"netns,omitempty"`      // netns path the qemu process runs in (CNI); "" otherwise

	// runtime
	PID int `json:"pid"`

	// state
	Snapshots []string `json:"snapshots,omitempty"` // qcow2-internal snapshot tags, newest last

	// timestamps
	Created string `json:"created"`
}
