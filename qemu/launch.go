// Package qemu builds and launches the qemu-system-x86_64 command for booting
// macOS (Tahoe 26) via OpenCore on an x86 Linux/KVM host.
package qemu

// Spec is the per-VM input for launching a macOS guest.
type Spec struct {
	Name    string // VM name
	Image   string // golden qcow2 (a per-VM overlay is derived from it at create time)
	CPUs    int    // vCPU count
	Memory  string // e.g. "8G"
	VNCDisp int    // VNC display number (n => host :590n); 0 disables
	SSHPort int    // host port forwarded to guest :22; 0 disables
}

// Args returns the qemu-system-x86_64 argument vector for the macOS guest.
//
// TODO(P0/P1): populate from the validated LongQT-sea / OSX-KVM recipe:
//
//	-machine q35,accel=kvm
//	-cpu Skylake-Client-v4,vendor=GenuineIntel,+invtsc,...  (AMD hosts need extra flags)
//	-drive if=pflash OVMF_CODE.fd + per-VM OVMF_VARS.fd
//	OpenCore boot disk + the macOS qcow2 (virtio-blk or AHCI)
//	-device virtio-tablet-pci          (LongQT cursor-freeze workaround)
//	-netdev user,hostfwd=tcp::<SSHPort>-:22 -device virtio-net|vmxnet3
//	-vnc :<VNCDisp>                     (headless)
//	-qmp unix:<runDir>/qmp.sock,server,nowait
func (s Spec) Args() []string {
	return nil
}
