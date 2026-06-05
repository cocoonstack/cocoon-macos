// Package qemu builds and launches the qemu-system-x86_64 command for booting
// macOS (Tahoe 26) via OpenCore on an x86 Linux/KVM host. The argument vector is
// the validated OSX-KVM recipe (Skylake-Client CPU spoofing GenuineIntel +
// isa-applesmc OSK + OVMF + an OpenCore boot disk), proven to boot Tahoe in CI.
package qemu

import (
	"fmt"
	"strings"
)

// OSK is the Apple SMC key required for macOS guests (public, from OSX-KVM).
const OSK = "ourhardworkbythesewordsguardedpleasedontsteal(c)AppleComputerInc"

// macOSCPU is the -cpu model for macOS Sequoia/Tahoe (older Penryn fails on 26).
const macOSCPU = "Skylake-Client,-hle,-rtm,kvm=on,vendor=GenuineIntel,+invtsc," +
	"vmware-cpuid-freq=on,+ssse3,+sse4.2,+popcnt,+avx,+aes,+xsave,+xsaveopt," +
	// PCID/INVPCID cut TLB-flush cost on the guest's frequent context switches; tsc-deadline gives a
	// one-shot LAPIC timer (fewer timer-related VM exits); rdtscp/xsavec round out what macOS expects.
	// Most are already in the Skylake-Client base — affirmed here so a base-model change can't drop them.
	"+pcid,+invpcid,+tsc-deadline,+rdtscp,+xsavec,check"

// Spec is the per-VM input for launching a macOS guest from a golden qcow2.
type Spec struct {
	Name      string // VM name
	Disk      string // per-VM macOS qcow2 (an overlay on the golden image)
	OpenCore  string // OpenCore.qcow2 (boot loader)
	OVMFCode  string // OVMF_CODE_4M.fd (read-only firmware)
	OVMFVars  string // per-VM OVMF_VARS.fd (writable NVRAM)
	CPUs      int    // vCPU count
	Memory    string // MiB, e.g. "8192"
	VNCDisp   int    // VNC display number (n => host 127.0.0.1:590n); <0 disables
	SSHPort   int    // host port forwarded to guest :22; 0 disables
	MonSock   string // optional HMP monitor unix socket
	QMPSock   string // optional QMP unix socket
	MAC       string // optional guest NIC MAC (set to the SMBIOS ROM for --random-smbios)
	VNCPass   string // optional VNC password (enables QEMU password auth; set via monitor after launch)
	Tap       string // optional pre-created host TAP ifname; set => bridged/routed (-netdev tap) instead of user-mode
	Hugepages bool   // back guest RAM with 2 MiB hugepages (needs host hugepages reserved); off => default RAM
}

// Args returns the qemu-system-x86_64 argument vector for the macOS guest.
func (s Spec) Args() []string {
	cores := max(s.CPUs/2, 1)
	// NVRAM rolls back under qemu-img snapshot only if it's qcow2; a raw .fd is loaded as raw.
	varsFmt := "raw"
	if strings.HasSuffix(s.OVMFVars, ".qcow2") {
		varsFmt = "qcow2"
	}
	// Hugepages (opt-in): back guest RAM with 2 MiB hugetlb pages to cut TLB/EPT pressure on a
	// memory-heavy GUI guest. Needs hugepages reserved on the host (else qemu won't start), so it's
	// off by default — default anonymous guest RAM is already THP-eligible.
	machine := "q35"
	var memBackend []string
	if s.Hugepages {
		memBackend = []string{"-object", "memory-backend-memfd,id=pc.ram,size=" + s.Memory + "M,hugetlb=on,hugetlbsize=2M,prealloc=on,share=on"}
		machine = "q35,memory-backend=pc.ram"
	}
	a := []string{
		"-enable-kvm", "-m", s.Memory,
		"-cpu", macOSCPU,
		"-machine", machine,
		"-smp", fmt.Sprintf("%d,cores=%d,sockets=1", s.CPUs, cores),
		"-device", "qemu-xhci,id=xhci",
		"-device", "usb-kbd,bus=xhci.0",
		"-device", "usb-tablet,bus=xhci.0",
		"-device", "isa-applesmc,osk=" + OSK,
		"-drive", "if=pflash,format=raw,readonly=on,file=" + s.OVMFCode,
		"-drive", "if=pflash,format=" + varsFmt + ",file=" + s.OVMFVars,
		"-smbios", "type=2",
		"-device", "ich9-ahci,id=sata",
		"-drive", "id=OpenCoreBoot,if=none,snapshot=on,format=qcow2,aio=io_uring,file=" + s.OpenCore,
		"-device", "ide-hd,bus=sata.2,drive=OpenCoreBoot",
		// MacHDD perf: io_uring beats the default threads aio backend; cache=writeback uses host RAM to
		// mask qcow2-overlay + cloud-disk (GCP PD) latency — do NOT use cache=none here, O_DIRECT would
		// hit the network disk on every I/O; discard/detect-zeroes reclaim freed clusters (also what the
		// slim stage depends on). macOS has no virtio-blk driver, so the OS disk stays on AHCI/IDE.
		"-drive", "id=MacHDD,if=none,format=qcow2,cache=writeback,aio=io_uring,discard=unmap,detect-zeroes=unmap,file=" + s.Disk,
		"-device", "ide-hd,bus=sata.4,drive=MacHDD",
		"-device", "vmware-svga",
	}
	a = append(memBackend, a...) // -object must precede the -machine memory-backend reference
	switch {
	case s.Tap != "":
		// attach to a pre-created host TAP (cocoon's network plane / a bridge owns IP+forwarding);
		// the macOS virtio-net front-end is unchanged, the guest just re-DHCPs off the bridge
		a = append(a, "-netdev", "tap,id=net0,ifname="+s.Tap+",script=no,downscript=no")
	case s.SSHPort > 0:
		a = append(a, "-netdev", fmt.Sprintf("user,id=net0,hostfwd=tcp::%d-:22", s.SSHPort))
	default:
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
		// -k en-us: translate received VNC keysyms through the en-US keymap, which
		// encodes the shift state for shifted keysyms. Without it, QEMU's default
		// keysym mapping drops the shift on shifted characters coming from the
		// guacamole VNC keyboard (V->v, &->7, $(->$9 — the "keyboard garbled" bug).
		a = append(a, "-k", "en-us", "-display", "none", "-vnc", vnc)
	}
	if s.MonSock != "" {
		a = append(a, "-monitor", "unix:"+s.MonSock+",server,nowait")
	}
	if s.QMPSock != "" {
		a = append(a, "-qmp", "unix:"+s.QMPSock+",server,nowait")
	}
	return a
}
