// Package qemu builds and launches the qemu-system-x86_64 command for booting
// macOS (Sequoia 15 / Tahoe 26) via OpenCore on an x86 Linux/KVM host. The argument vector spoofs a
// GenuineIntel Skylake-Client-v4 + isa-applesmc OSK + OVMF + an OpenCore boot disk; with the LongQT
// OpenCore EFI it boots identically on Intel and AMD hosts.
package qemu

import (
	"fmt"
	"strings"
)

const (
	// OSK is the Apple SMC key required for macOS guests (public, from OSX-KVM).
	OSK = "ourhardworkbythesewordsguardedpleasedontsteal(c)AppleComputerInc"

	// macOSCPU spoofs a GenuineIntel Skylake-Client-v4 — the model the LongQT OpenCore expects, which
	// boots macOS identically on Intel and AMD hosts (AMD support lives in the OpenCore EFI, not the
	// -cpu). kvm=on exposes the hypervisor CPUID leaf macOS reads for the invtsc frequency.
	macOSCPU = "Skylake-Client-v4,vendor=GenuineIntel,kvm=on"

	// ahciDriveOpts is the shared -drive tuning for every writable macOS AHCI/IDE disk (OS disk +
	// data disks; macOS has no virtio-blk). io_uring beats the default threads aio backend, and
	// cache=writeback uses host RAM to mask qcow2-overlay + cloud-disk (GCP PD) latency — do NOT use
	// cache=none, O_DIRECT would hit the network disk on every I/O; discard/detect-zeroes reclaim
	// freed clusters (the slim stage depends on it).
	ahciDriveOpts = "if=none,format=qcow2,cache=writeback,aio=io_uring,discard=unmap,detect-zeroes=unmap"
)

// Spec is the per-VM input for launching a macOS guest from a golden qcow2.
type Spec struct {
	Name string

	CPUs      int
	Memory    string   // MiB, e.g. "8192"
	VNCDisp   int      // n => host 127.0.0.1:590n; <0 disables
	VNCPass   string   // set via the monitor post-launch (macOS Screen Sharing needs password auth)
	SSHPort   int      // host port forwarded to guest :22; 0 disables
	Hugepages bool     // needs host hugepages reserved; off => default RAM
	DataDisks []string // extra qcow2 data disks; attached on the AHCI ports MacHDD/OpenCore leave free

	Disk     string
	OpenCore string
	OVMFCode string
	OVMFVars string
	MAC      string // set to the SMBIOS ROM for --random-smbios
	Tap      string // pre-created host TAP; set => -netdev tap (bridged/routed), empty => user-mode SLIRP

	MonSock string
	QMPSock string
}

// IsQcow2NVRAM reports whether the NVRAM file is qcow2 (by suffix). Single source of the rule
// that decides both the pflash -drive format below and whether NVRAM participates in
// qcow2-internal snapshots (a raw .fd can't hold them) — the two must stay in lockstep.
func IsQcow2NVRAM(path string) bool {
	return strings.HasSuffix(path, ".qcow2")
}

// Args returns the qemu-system-x86_64 argument vector for the macOS guest.
func (s Spec) Args() []string {
	cores := max(s.CPUs/2, 1)
	varsFmt := "raw"
	if IsQcow2NVRAM(s.OVMFVars) {
		varsFmt = "qcow2"
	}
	// 2 MiB hugetlb pages cut TLB/EPT pressure on a memory-heavy GUI guest, but the host must have
	// them reserved (else qemu won't start); plain anonymous RAM is already THP-eligible.
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
		"-drive", "id=MacHDD," + ahciDriveOpts + ",file=" + s.Disk,
		"-device", "ide-hd,bus=sata.4,drive=MacHDD",
		"-device", "vmware-svga",
	}
	a = append(memBackend, a...) // -object must precede the -machine memory-backend reference
	// data disks ride the SATA ports OpenCoreBoot (sata.2) and MacHDD (sata.4) leave free; the count
	// is capped at 4 upstream so the index never runs past dataDiskPorts.
	dataDiskPorts := []int{0, 1, 3, 5}
	for i, path := range s.DataDisks {
		if i >= len(dataDiskPorts) {
			break
		}
		id := fmt.Sprintf("DataDisk%d", i)
		a = append(a,
			"-drive", fmt.Sprintf("id=%s,%s,file=%s", id, ahciDriveOpts, path),
			"-device", fmt.Sprintf("ide-hd,bus=sata.%d,drive=%s", dataDiskPorts[i], id))
	}
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
			vnc += ",password=on" // macOS Screen Sharing can't use QEMU's bare "None" auth
		}
		// -k en-us maps received VNC keysyms through the en-US keymap so shifted keys aren't dropped
		// (the guacamole "keyboard garbled" bug: V->v, &->7).
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
