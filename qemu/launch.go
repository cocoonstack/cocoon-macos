// Package qemu builds and launches the qemu-system-x86_64 command that boots macOS (Sequoia 15 / Tahoe 26) via OpenCore on an x86 Linux/KVM host, spoofing a GenuineIntel Skylake-Client (TSX off, invariant TSC via CPUID) plus isa-applesmc OSK, OVMF and an OpenCore boot disk; the LongQT OpenCore EFI makes it boot identically on Intel and AMD.
package qemu

import (
	"fmt"
	"strings"
)

const (
	// OSK is the Apple SMC key required for macOS guests (public, from OSX-KVM).
	OSK = "ourhardworkbythesewordsguardedpleasedontsteal(c)AppleComputerInc"

	// macOSCPU is the -cpu for macOS Sequoia/Tahoe; every token is load-bearing — a stripped "Skylake-Client-v4" makes a fresh image's first boot spin forever (regression dacf35c): -hle,-rtm drop TSX (macOS spins on it under nested KVM); +invtsc,vmware-cpuid-freq=on feed the TSC frequency via CPUID so macOS skips self-calibration; vendor=GenuineIntel is mandatory; the +perf flags are affirmed with check= so a base-model bump can't drop them. AMD support comes from the LongQT OpenCore EFI, not the -cpu.
	macOSCPU = "Skylake-Client,-hle,-rtm,kvm=on,vendor=GenuineIntel,+invtsc,vmware-cpuid-freq=on," +
		"+ssse3,+sse4.2,+popcnt,+avx,+aes,+xsave,+xsaveopt," +
		"+pcid,+invpcid,+tsc-deadline,+rdtscp,+xsavec,check"

	// ahciDriveOpts tunes every writable disk (macOS has no virtio-blk): io_uring beats the threads aio backend; cache=writeback masks qcow2 + cloud-disk latency (cache=none would hit the network disk on every I/O); discard/detect-zeroes reclaim freed clusters (slim depends on it).
	ahciDriveOpts = "if=none,format=qcow2,cache=writeback,aio=io_uring,discard=unmap,detect-zeroes=unmap"
)

// Spec is the per-VM input for launching a macOS guest from a golden qcow2.
type Spec struct {
	Name string

	CPUs         int
	Memory       string   // MiB, e.g. "8192"
	VNCDisp      int      // n => host 127.0.0.1:590n; <0 disables
	VNCSock      string   // when set, bind VNC to this unix socket instead of 127.0.0.1 (CNI: fronted by vncProxy)
	VNCPass      string   // set via the monitor post-launch (macOS Screen Sharing needs password auth)
	SSHPort      int      // host port forwarded to guest :22; 0 disables
	Hugepages    bool     // needs host hugepages reserved; off => default RAM
	ExitOnReboot bool     // exit QEMU on a guest reboot so an external owner can relaunch it cold
	DataDisks    []string // extra qcow2 data disks; attached on the AHCI ports MacHDD/OpenCore leave free

	Disk     string
	OpenCore string
	OVMFCode string
	OVMFVars string
	MAC      string // guest NIC MAC: SMBIOS ROM by default, CNI eth0 in CNI mode
	Tap      string // pre-created host TAP; set => -netdev tap (bridged/routed), empty => user-mode SLIRP

	MonSock string
	QMPSock string
}

// Args returns the qemu-system-x86_64 argument vector for the macOS guest.
func (s Spec) Args() []string {
	cores := max(s.CPUs/2, 1)
	varsFmt := "raw"
	if IsQcow2NVRAM(s.OVMFVars) {
		varsFmt = "qcow2"
	}
	// 2 MiB hugetlb pages cut TLB/EPT pressure but must be pre-reserved on the host (else qemu won't start)
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
	if s.ExitOnReboot {
		// A macOS warm reset can stall indefinitely during early boot under KVM.
		// Exit QEMU so an external owner can relaunch the guest cold instead.
		a = append(a, "-no-reboot")
	}
	a = append(memBackend, a...) // -object must precede the -machine memory-backend reference
	// the SATA ports OpenCoreBoot (sata.2) and MacHDD (sata.4) leave free; the count is capped at 4 upstream
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
	// without -display QEMU defaults to GTK and aborts ("gtk initialization failed")
	a = append(a, "-display", "none")
	if s.VNCDisp >= 0 {
		var vnc string
		if s.VNCSock != "" {
			vnc = "unix:" + s.VNCSock
		} else {
			vnc = fmt.Sprintf("127.0.0.1:%d", s.VNCDisp)
		}
		if s.VNCPass != "" {
			vnc += ",password=on" // macOS Screen Sharing can't use QEMU's bare "None" auth
		}
		// -k en-us maps VNC keysyms so shifted keys aren't dropped (guacamole "keyboard garbled" bug)
		a = append(a, "-k", "en-us", "-vnc", vnc)
	}
	if s.MonSock != "" {
		a = append(a, "-monitor", "unix:"+s.MonSock+",server,nowait")
	}
	if s.QMPSock != "" {
		a = append(a, "-qmp", "unix:"+s.QMPSock+",server,nowait")
	}
	return a
}

// IsQcow2NVRAM is the single source of the qcow2-NVRAM rule: the pflash -drive format and NVRAM's participation in internal snapshots must stay in lockstep.
func IsQcow2NVRAM(path string) bool {
	return strings.HasSuffix(path, ".qcow2")
}
