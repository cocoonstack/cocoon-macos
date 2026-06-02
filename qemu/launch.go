// Package qemu builds and launches the qemu-system-x86_64 command for booting
// macOS (Tahoe 26) via OpenCore on an x86 Linux/KVM host. The argument vector is
// the validated OSX-KVM recipe (Skylake-Client CPU spoofing GenuineIntel +
// isa-applesmc OSK + OVMF + an OpenCore boot disk), proven to boot Tahoe in CI.
package qemu

import "fmt"

// OSK is the Apple SMC key required for macOS guests (public, from OSX-KVM).
const OSK = "ourhardworkbythesewordsguardedpleasedontsteal(c)AppleComputerInc"

// macOSCPU is the -cpu model for macOS Sequoia/Tahoe (older Penryn fails on 26).
const macOSCPU = "Skylake-Client,-hle,-rtm,kvm=on,vendor=GenuineIntel,+invtsc," +
	"vmware-cpuid-freq=on,+ssse3,+sse4.2,+popcnt,+avx,+aes,+xsave,+xsaveopt,check"

// Spec is the per-VM input for launching a macOS guest from a golden qcow2.
type Spec struct {
	Name     string // VM name
	Disk     string // per-VM macOS qcow2 (an overlay on the golden image)
	OpenCore string // OpenCore.qcow2 (boot loader)
	OVMFCode string // OVMF_CODE_4M.fd (read-only firmware)
	OVMFVars string // per-VM OVMF_VARS.fd (writable NVRAM)
	CPUs     int    // vCPU count
	Memory   string // MiB, e.g. "8192"
	VNCDisp  int    // VNC display number (n => host 127.0.0.1:590n); <0 disables
	SSHPort  int    // host port forwarded to guest :22; 0 disables
	MonSock  string // optional HMP monitor unix socket
	QMPSock  string // optional QMP unix socket
	MAC      string // optional guest NIC MAC (set to the SMBIOS ROM for --random-smbios)
	VNCPass  string // optional VNC password (enables QEMU password auth; set via monitor after launch)
}

// Args returns the qemu-system-x86_64 argument vector for the macOS guest.
func (s Spec) Args() []string {
	cores := max(s.CPUs/2, 1)
	a := []string{
		"-enable-kvm", "-m", s.Memory,
		"-cpu", macOSCPU,
		"-machine", "q35",
		"-smp", fmt.Sprintf("%d,cores=%d,sockets=1", s.CPUs, cores),
		"-device", "qemu-xhci,id=xhci",
		"-device", "usb-kbd,bus=xhci.0",
		"-device", "usb-tablet,bus=xhci.0",
		"-device", "isa-applesmc,osk=" + OSK,
		"-drive", "if=pflash,format=raw,readonly=on,file=" + s.OVMFCode,
		"-drive", "if=pflash,format=raw,file=" + s.OVMFVars,
		"-smbios", "type=2",
		"-device", "ich9-ahci,id=sata",
		"-drive", "id=OpenCoreBoot,if=none,snapshot=on,format=qcow2,file=" + s.OpenCore,
		"-device", "ide-hd,bus=sata.2,drive=OpenCoreBoot",
		"-drive", "id=MacHDD,if=none,format=qcow2,file=" + s.Disk,
		"-device", "ide-hd,bus=sata.4,drive=MacHDD",
		"-device", "vmware-svga",
	}
	if s.SSHPort > 0 {
		a = append(a, "-netdev", fmt.Sprintf("user,id=net0,hostfwd=tcp::%d-:22", s.SSHPort))
	} else {
		a = append(a, "-netdev", "user,id=net0")
	}
	nic := "virtio-net-pci,netdev=net0,id=net0"
	if s.MAC != "" {
		nic += ",mac=" + s.MAC
	}
	a = append(a, "-device", nic)
	if s.VNCDisp >= 0 {
		vnc := fmt.Sprintf("127.0.0.1:%d", s.VNCDisp)
		if s.VNCPass != "" {
			vnc += ",password=on" // macOS Screen Sharing can't use QEMU's bare "None" auth; the password is set via monitor post-launch
		}
		a = append(a, "-display", "none", "-vnc", vnc)
	}
	if s.MonSock != "" {
		a = append(a, "-monitor", "unix:"+s.MonSock+",server,nowait")
	}
	if s.QMPSock != "" {
		a = append(a, "-qmp", "unix:"+s.QMPSock+",server,nowait")
	}
	return a
}
