package qemu

import (
	"slices"
	"strings"
	"testing"
)

// argVals returns each token immediately following flag in args.
func argVals(args []string, flag string) []string {
	var out []string
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			out = append(out, args[i+1])
		}
	}
	return out
}

func TestArgsNetModes(t *testing.T) {
	base := Spec{Name: "m", Disk: "/v/disk.qcow2", OpenCore: "/v/oc.qcow2", OVMFCode: "/v/code.fd", OVMFVars: "/v/vars.fd", CPUs: 4, Memory: "8192"}
	tests := []struct {
		name   string
		mutate func(s *Spec)
		want   string
	}{
		{"user-mode without ssh-port is bare SLIRP", func(_ *Spec) {}, "user,id=net0"},
		{"user-mode with ssh-port adds hostfwd", func(s *Spec) { s.SSHPort = 2222 }, "user,id=net0,hostfwd=tcp::2222-:22"},
		{"tap attaches to the pre-created TAP", func(s *Spec) { s.Tap = "tap0" }, "tap,id=net0,ifname=tap0,script=no,downscript=no"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := base
			tt.mutate(&s)
			if got := argVals(s.Args(), "-netdev"); len(got) != 1 || got[0] != tt.want {
				t.Errorf("got %v, want %q", got, tt.want)
			}
		})
	}
}

func TestArgsMACAndVNC(t *testing.T) {
	s := Spec{Disk: "/v/d.qcow2", OpenCore: "/v/oc.qcow2", OVMFCode: "/v/c.fd", OVMFVars: "/v/v.fd", CPUs: 2, Memory: "4096", MAC: "aa:bb:cc:dd:ee:ff", VNCDisp: 9, VNCPass: "secret"}
	args := s.Args()
	nics := argVals(args, "-device")
	if !slices.ContainsFunc(nics, func(d string) bool {
		return strings.Contains(d, "virtio-net-pci") && strings.Contains(d, "mac=aa:bb:cc:dd:ee:ff")
	}) {
		t.Fatalf("nic mac not on virtio-net-pci: %v", nics)
	}
	// Screen Sharing rejects bare None auth, so a password forces password=on
	if got := argVals(args, "-vnc"); len(got) != 1 || got[0] != "127.0.0.1:9,password=on" {
		t.Fatalf("vnc: %v", got)
	}
	// VNC enabled => -k en-us so the guacamole keyboard's shifted keysyms map correctly
	if got := argVals(args, "-k"); len(got) != 1 || got[0] != "en-us" {
		t.Errorf("keymap: got %v, want [en-us]", got)
	}

	s.VNCPass = ""
	if got := argVals(s.Args(), "-vnc"); got[0] != "127.0.0.1:9" {
		t.Fatalf("vnc no-pass: %v", got)
	}
}

func TestArgsOVMFVarsFormat(t *testing.T) {
	// qcow2 NVRAM needs format=qcow2 so qemu-img snapshot can roll it back; a raw .fd stays raw
	s := Spec{Disk: "/v/d.qcow2", OpenCore: "/v/oc.qcow2", OVMFCode: "/v/c.fd", OVMFVars: "/v/vars.qcow2", CPUs: 1, Memory: "2048", VNCDisp: -1}
	if !slices.ContainsFunc(s.Args(), func(a string) bool { return strings.Contains(a, "if=pflash,format=qcow2,file=/v/vars.qcow2") }) {
		t.Fatalf("qcow2 NVRAM not format=qcow2: %v", s.Args())
	}
	s.OVMFVars = "/v/vars.fd"
	if !slices.ContainsFunc(s.Args(), func(a string) bool { return strings.Contains(a, "if=pflash,format=raw,file=/v/vars.fd") }) {
		t.Fatalf("raw NVRAM not format=raw: %v", s.Args())
	}
}

func TestArgsDiskTuning(t *testing.T) {
	s := Spec{Disk: "/v/d.qcow2", OpenCore: "/v/oc.qcow2", OVMFCode: "/v/c.fd", OVMFVars: "/v/v.fd", CPUs: 2, Memory: "4096", VNCDisp: -1}
	drives := argVals(s.Args(), "-drive")
	// MacHDD must carry the perf knobs
	if !slices.ContainsFunc(drives, func(d string) bool {
		return strings.Contains(d, "id=MacHDD") && strings.Contains(d, "cache=writeback") &&
			strings.Contains(d, "aio=io_uring") && strings.Contains(d, "discard=unmap") &&
			strings.Contains(d, "detect-zeroes=unmap")
	}) {
		t.Fatalf("MacHDD missing perf tuning: %v", drives)
	}
}

func TestArgsHugepages(t *testing.T) {
	base := Spec{Disk: "/v/d.qcow2", OpenCore: "/v/oc.qcow2", OVMFCode: "/v/c.fd", OVMFVars: "/v/v.fd", CPUs: 2, Memory: "4096", VNCDisp: -1}
	// off by default: no memory-backend object, plain -machine q35
	if slices.ContainsFunc(base.Args(), func(a string) bool { return strings.Contains(a, "memory-backend") }) {
		t.Fatalf("hugepages off must not add a memory-backend: %v", base.Args())
	}
	if got := argVals(base.Args(), "-machine"); len(got) != 1 || got[0] != "q35" {
		t.Fatalf("machine without hugepages: %v", got)
	}
	// on: memfd hugetlb backend sized to the guest RAM, and -machine references it
	h := base
	h.Hugepages = true
	if !slices.ContainsFunc(h.Args(), func(a string) bool {
		return strings.Contains(a, "memory-backend-memfd") && strings.Contains(a, "hugetlb=on") && strings.Contains(a, "size=4096M")
	}) {
		t.Fatalf("hugepages on missing memfd backend: %v", h.Args())
	}
	if got := argVals(h.Args(), "-machine"); len(got) != 1 || got[0] != "q35,memory-backend=pc.ram" {
		t.Fatalf("machine memory-backend: %v", got)
	}
}

func TestArgsCPUPerfFlags(t *testing.T) {
	s := Spec{Disk: "/v/d.qcow2", OpenCore: "/v/oc.qcow2", OVMFCode: "/v/c.fd", OVMFVars: "/v/v.fd", CPUs: 4, Memory: "4096", VNCDisp: -1}
	cpu := argVals(s.Args(), "-cpu")
	if len(cpu) != 1 {
		t.Fatalf("-cpu count: %v", cpu)
	}
	// macOS contract (GenuineIntel + invtsc) plus the perf flags must all be present
	for _, f := range []string{"vendor=GenuineIntel", "+invtsc", "+pcid", "+invpcid", "+tsc-deadline", "+rdtscp"} {
		if !strings.Contains(cpu[0], f) {
			t.Fatalf("-cpu missing %s: %s", f, cpu[0])
		}
	}
}
