package qemu

import (
	"slices"
	"strings"
	"testing"
)

// argAfter returns the token following the i-th occurrence of flag (e.g. the value of a given
// -device), or "" — lets us assert specific arg pairs without pinning the whole vector order.
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

	// user-mode, no ssh-port => bare SLIRP
	if got := argVals(base.Args(), "-netdev"); len(got) != 1 || got[0] != "user,id=net0" {
		t.Fatalf("user-mode netdev: %v", got)
	}
	// user-mode + ssh-port => hostfwd
	s := base
	s.SSHPort = 2222
	if got := argVals(s.Args(), "-netdev"); got[0] != "user,id=net0,hostfwd=tcp::2222-:22" {
		t.Fatalf("hostfwd netdev: %v", got)
	}
	// tap => attach to the pre-created TAP, never user-mode
	s = base
	s.Tap = "tap0"
	if got := argVals(s.Args(), "-netdev"); got[0] != "tap,id=net0,ifname=tap0,script=no,downscript=no" {
		t.Fatalf("tap netdev: %v", got)
	}
}

func TestArgsMACAndVNC(t *testing.T) {
	s := Spec{Disk: "/v/d.qcow2", OpenCore: "/v/oc.qcow2", OVMFCode: "/v/c.fd", OVMFVars: "/v/v.fd", CPUs: 2, Memory: "4096", MAC: "aa:bb:cc:dd:ee:ff", VNCDisp: 9, VNCPass: "secret"}
	args := s.Args()
	// the NIC device carries the MAC
	nics := argVals(args, "-device")
	if !slices.ContainsFunc(nics, func(d string) bool {
		return strings.Contains(d, "virtio-net-pci") && strings.Contains(d, "mac=aa:bb:cc:dd:ee:ff")
	}) {
		t.Fatalf("nic mac not on virtio-net-pci: %v", nics)
	}
	// VNC with a password enables QEMU password auth (Screen Sharing can't use bare None auth)
	if got := argVals(args, "-vnc"); len(got) != 1 || got[0] != "127.0.0.1:9,password=on" {
		t.Fatalf("vnc: %v", got)
	}

	// no password => no password=on
	s.VNCPass = ""
	if got := argVals(s.Args(), "-vnc"); got[0] != "127.0.0.1:9" {
		t.Fatalf("vnc no-pass: %v", got)
	}
}

func TestArgsOVMFVarsFormat(t *testing.T) {
	// a qcow2 NVRAM must be declared format=qcow2 (so qemu-img snapshot can roll it back); a raw .fd raw
	s := Spec{Disk: "/v/d.qcow2", OpenCore: "/v/oc.qcow2", OVMFCode: "/v/c.fd", OVMFVars: "/v/vars.qcow2", CPUs: 1, Memory: "2048", VNCDisp: -1}
	if !slices.ContainsFunc(s.Args(), func(a string) bool { return strings.Contains(a, "if=pflash,format=qcow2,file=/v/vars.qcow2") }) {
		t.Fatalf("qcow2 NVRAM not format=qcow2: %v", s.Args())
	}
	s.OVMFVars = "/v/vars.fd"
	if !slices.ContainsFunc(s.Args(), func(a string) bool { return strings.Contains(a, "if=pflash,format=raw,file=/v/vars.fd") }) {
		t.Fatalf("raw NVRAM not format=raw: %v", s.Args())
	}
}
