package vm

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/cocoonstack/cocoon/config"
	"github.com/cocoonstack/cocoon/images/cloudimg"
	"github.com/cocoonstack/cocoon/types"
	"github.com/cocoonstack/cocoon/utils"

	"github.com/cocoonstack/cocoon-macos/qemu"
)

const (
	qemuBinary      = "qemu-system-x86_64"
	stopGracePeriod = 10 * time.Second
	defaultStateDir = "/var/lib/cocoon-macos" // mirrors cocoon's /var/lib/cocoon; override via --state-dir or $COCOON_MACOS_HOME
)

// resolveBase returns the immutable base qcow2 for IMAGE: a direct filesystem path (legacy), else
// an image ref resolved through cocoon's cloudimg store (returns the content-addressed blob + its
// digest). Per-VM overlays are baked on this base, which stays read-only.
func resolveBase(cmd *cobra.Command, image, name string) (string, string, error) {
	if _, err := os.Stat(image); err == nil {
		return image, "", nil
	}
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	root := stateDir(cmd)
	ensureCloudimgFirmware(root)
	store, err := cloudimg.New(ctx, &config.Config{RootDir: root, DNS: "8.8.8.8,1.1.1.1"})
	if err != nil {
		return "", "", err
	}
	vm := &types.VMConfig{Config: types.Config{Image: image}, Name: name}
	sc, _, err := store.Config(ctx, []*types.VMConfig{vm})
	if err != nil {
		return "", "", fmt.Errorf("resolve image %q (not a file, not in the store): %w", image, err)
	}
	if len(sc) == 0 || len(sc[0]) == 0 {
		return "", "", fmt.Errorf("image %q resolved to no disk", image)
	}
	return sc[0][0].Path, vm.ImageDigest, nil
}

// ensureCloudimgFirmware writes a placeholder CLOUDHV.fd where cocoon's cloudimg.Config insists a
// UEFI firmware exists (it targets cloud-hypervisor). cocoon-macos boots via OVMF + OpenCore and
// DISCARDS that BootConfig, so the file is never read — it only unblocks Config's validation.
func ensureCloudimgFirmware(rootDir string) {
	fw := filepath.Join(rootDir, "firmware", "CLOUDHV.fd")
	if utils.ValidFile(fw) {
		return
	}
	if err := os.MkdirAll(filepath.Dir(fw), 0o755); err == nil {
		_ = os.WriteFile(fw, []byte("placeholder: cocoon-macos boots via OVMF, not CLOUDHV\n"), 0o644)
	}
}

// firmwareDir is the managed home for shared loader/firmware assets (OpenCore base, OVMF_CODE,
// OVMF_VARS template) — populated by `cocoon-macos firmware install`, reused across all VMs.
func firmwareDir(cmd *cobra.Command) string {
	return filepath.Join(stateDir(cmd), "firmware")
}

// resolveFirmware returns the OpenCore loader + OVMF code/vars: an explicit flag always wins,
// else the shared managed copy under <state-dir>/firmware/. OVMF_CODE is shared read-only; the
// OpenCore + OVMF_VARS here are the base/template that per-VM copies (overlay/NVRAM) derive from.
func resolveFirmware(cmd *cobra.Command) (opencore, code, vars string, err error) {
	fw := firmwareDir(cmd)
	pick := func(flag, managed string) string {
		if v, _ := cmd.Flags().GetString(flag); v != "" {
			return v
		}
		return filepath.Join(fw, managed)
	}
	opencore, code, vars = pick("opencore", "OpenCore.qcow2"), pick("ovmf-code", "OVMF_CODE.fd"), pick("ovmf-vars", "OVMF_VARS.fd")
	for _, p := range []string{opencore, code, vars} {
		if !utils.ValidFile(p) {
			return "", "", "", fmt.Errorf("firmware not found: %s — pass --opencore/--ovmf-code/--ovmf-vars or run `cocoon-macos firmware install`", p)
		}
	}
	return opencore, code, vars, nil
}

// Handler implements Actions by cloning a golden macOS qcow2 (copy-on-write overlay)
// and launching qemu-system-x86_64 on an x86 Linux/KVM host.
type Handler struct{}

func NewHandler() *Handler { return &Handler{} }

type record struct {
	Name        string       `json:"name"`
	Image       string       `json:"image"`
	ImageDigest string       `json:"image_digest,omitempty"`
	Disk        string       `json:"disk"`
	OpenCore    string       `json:"opencore"`
	OVMFCode    string       `json:"ovmf_code"`
	OVMFVars    string       `json:"ovmf_vars"`
	CPUs        int          `json:"cpus"`
	Memory      string       `json:"memory"`
	VNCDisp     int          `json:"vnc"`
	SSHPort     int          `json:"ssh_port"`
	PID         int          `json:"pid"`
	Created     string       `json:"created"`
	MAC         string       `json:"mac,omitempty"`
	SMBIOS      *qemu.SMBIOS `json:"smbios,omitempty"`
	VNCPass     string       `json:"vnc_password,omitempty"`
	NetMode     string       `json:"net_mode,omitempty"`
	Tap         string       `json:"tap,omitempty"`
	Hugepages   bool         `json:"hugepages,omitempty"`  // back guest RAM with 2 MiB hugepages
	TapOwned    bool         `json:"tap_owned,omitempty"`  // cocoon auto-created the TAP (tear down on rm); false for user --tap
	BridgeDev   string       `json:"bridge_dev,omitempty"` // bridge to enslave the TAP to; persisted so rm can tear down without --bridge
	Netns       string       `json:"netns,omitempty"`      // netns path the qemu process runs in (CNI); "" otherwise
	VMID        string       `json:"vmid,omitempty"`       // random id for the network plane (cocoon VMIDPrefix); != Name
	Snapshots   []string     `json:"snapshots,omitempty"`  // qcow2-internal snapshot tags, newest last
}

func stateDir(cmd *cobra.Command) string {
	if d, _ := cmd.Flags().GetString("state-dir"); d != "" {
		return d
	}
	if d := os.Getenv("COCOON_MACOS_HOME"); d != "" {
		return d
	}
	return defaultStateDir // /var/lib/cocoon-macos, mirroring cocoon's /var/lib/cocoon
}

func vmDir(cmd *cobra.Command, name string) string {
	return filepath.Join(stateDir(cmd), "vms", name)
}

func loadRec(dir string) (*record, error) {
	b, err := os.ReadFile(filepath.Join(dir, "vm.json"))
	if err != nil {
		return nil, err
	}
	var r record
	return &r, json.Unmarshal(b, &r)
}

func saveRec(dir string, r *record) error {
	b, _ := json.MarshalIndent(r, "", "  ")
	return os.WriteFile(filepath.Join(dir, "vm.json"), b, 0o644)
}

func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o644)
}

func ctxOf(cmd *cobra.Command) context.Context {
	if ctx := cmd.Context(); ctx != nil {
		return ctx
	}
	return context.Background()
}

// newVMID is the network-plane id (cocoon truncates it to 8 chars via VMIDPrefix); random so
// same-second Names don't collide.
func newVMID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// running reports whether the VM's qemu process is alive AND is actually this VM's qemu (argv0 +
// the overlay path in /proc/<pid>/cmdline), so a recycled PID isn't mistaken for a live VM; falls
// back to a bare liveness probe on non-Linux.
func running(r *record) bool {
	return utils.VerifyProcessCmdline(r.PID, qemuBinary, r.Disk)
}

// terminate stops the VM's qemu process (PID-reuse-safe — verifies the cmdline before signaling);
// grace=0 is the --force path (SIGTERM, then immediate SIGKILL).
func terminate(ctx context.Context, r *record, grace time.Duration) {
	if r.PID > 0 {
		_ = utils.TerminateProcess(ctx, r.PID, qemuBinary, r.Disk, grace)
	}
}

// create derives a per-VM copy-on-write overlay on the golden image + writes the record.
func (h *Handler) create(cmd *cobra.Command, image string) (*record, error) {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "macos-" + time.Now().Format("20060102-150405")
	}
	oc, code, varsTmpl, err := resolveFirmware(cmd)
	if err != nil {
		return nil, err
	}
	dir := vmDir(cmd, name)
	if err = os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	ctx := ctxOf(cmd)
	base, digest, err := resolveBase(cmd, image, name)
	if err != nil {
		return nil, err
	}
	overlay := filepath.Join(dir, "disk.qcow2")
	// bake a per-VM CoW overlay on the immutable base (cocoon's storage convention; base stays RO)
	if err = utils.RunQemuImg(ctx, "create", "-f", "qcow2", "-F", "qcow2", "-b", base, overlay); err != nil {
		return nil, fmt.Errorf("bake overlay on %s: %w", base, err)
	}
	ovmfVars := filepath.Join(dir, "OVMF_VARS.fd")
	if err = copyFile(varsTmpl, ovmfVars); err != nil {
		return nil, fmt.Errorf("copy OVMF_VARS: %w", err)
	}
	cpus, _ := cmd.Flags().GetInt("cpus")
	mem, _ := cmd.Flags().GetString("memory")
	vnc, _ := cmd.Flags().GetInt("vnc")
	ssh, _ := cmd.Flags().GetInt("ssh-port")
	vncPass, _ := cmd.Flags().GetString("vnc-password")
	netMode, _ := cmd.Flags().GetString("net")
	tap, _ := cmd.Flags().GetString("tap")
	huge, _ := cmd.Flags().GetBool("hugepages")
	r := &record{
		Name: name, Image: image, ImageDigest: digest, Disk: overlay, OpenCore: oc, OVMFCode: code, OVMFVars: ovmfVars,
		CPUs: cpus, Memory: mem, VNCDisp: vnc, SSHPort: ssh, VNCPass: vncPass, NetMode: netMode, Tap: tap, Hugepages: huge,
		VMID: newVMID(), Created: time.Now().Format(time.RFC3339),
	}
	// SMBIOS before networking: assignSMBIOS sets r.MAC = ROM, which prepareNet keeps as the guest MAC.
	if random, _ := cmd.Flags().GetBool("random-smbios"); random {
		if err = assignSMBIOS(ctx, dir, oc, r); err != nil {
			return nil, err
		}
	}
	netTap, netns, mac, err := prepareNet(cmd, r)
	if err != nil {
		return nil, err
	}
	if r.MAC == "" {
		r.MAC = mac
	}
	if netTap != "" {
		r.Tap, r.Netns, r.TapOwned = netTap, netns, tap == "" // owned (auto-created) unless the user passed --tap
	}
	return r, saveRec(dir, r)
}

// assignSMBIOS gives the VM a unique identity by injecting PlatformInfo/Generic into a per-VM
// OpenCore that is a CoW OVERLAY on the shared base loader (ocBase) — only the injected delta is
// stored per-VM, so the 19MB base is reused, not copied N times.
func assignSMBIOS(ctx context.Context, dir, ocBase string, r *record) error {
	sm, err := qemu.RandomSMBIOS()
	if err != nil {
		return err
	}
	ocOverlay := filepath.Join(dir, "OpenCore.qcow2")
	if err := utils.RunQemuImg(ctx, "create", "-f", "qcow2", "-F", "qcow2", "-b", ocBase, ocOverlay); err != nil {
		return fmt.Errorf("bake OpenCore overlay on %s: %w", ocBase, err)
	}
	if err := qemu.InjectSMBIOS(ocOverlay, sm); err != nil {
		return fmt.Errorf("inject SMBIOS: %w", err)
	}
	r.OpenCore, r.SMBIOS, r.MAC = ocOverlay, &sm, sm.MAC()
	return nil
}

func (h *Handler) launch(cmd *cobra.Command, dir string, r *record) error {
	spec := qemu.Spec{
		Name: r.Name, Disk: r.Disk, OpenCore: r.OpenCore, OVMFCode: r.OVMFCode, OVMFVars: r.OVMFVars,
		CPUs: r.CPUs, Memory: r.Memory, VNCDisp: r.VNCDisp, SSHPort: r.SSHPort, MAC: r.MAC, VNCPass: r.VNCPass,
		Tap:       r.Tap, // set for tap/bridge/cni (a real host TAP); empty => user-mode SLIRP
		Hugepages: r.Hugepages,
		MonSock:   filepath.Join(dir, "monitor.sock"), QMPSock: filepath.Join(dir, "qmp.sock"),
	}
	pidfile := filepath.Join(dir, "qemu.pid")
	args := append(spec.Args(), "-daemonize", "-pidfile", pidfile)
	ensureNetnsLoopback(r)  // CNI: a fresh netns has lo DOWN, so qemu's -vnc 127.0.0.1 would fail to bind
	c := launchCmd(r, args) // CNI: wraps in `ip netns exec` so -netdev tap finds the in-netns TAP
	c.Stdout, c.Stderr = os.Stdout, os.Stderr
	if err := c.Run(); err != nil {
		teardownNet(cmd, r) // don't leak an auto-created TAP/netns on a failed launch
		return fmt.Errorf("qemu launch: %w", err)
	}
	if b, err := os.ReadFile(pidfile); err == nil {
		r.PID, _ = strconv.Atoi(strings.TrimSpace(string(b)))
	}
	if r.VNCPass != "" {
		if err := setVNCPassword(spec.MonSock, r.VNCPass); err != nil {
			fmt.Fprintf(os.Stderr, "warning: VNC password not set: %v\n", err)
		}
	}
	return saveRec(dir, r)
}

// setVNCPassword applies the VNC password over the HMP monitor (QEMU was started with
// password=on); macOS Screen Sharing needs password auth, not QEMU's default "None".
func setVNCPassword(monSock, pw string) error {
	var conn net.Conn
	var err error
	for range 50 {
		if conn, err = net.Dial("unix", monSock); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		return fmt.Errorf("dial monitor: %w", err)
	}
	defer func() { _ = conn.Close() }()
	// Wait for the HMP prompt before sending: right after -daemonize the monitor accepts the
	// connection but discards input until it is ready, so an early set_password is silently lost
	// (QEMU keeps password=on with no password set -> all auth fails).
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, ok := readUntil(conn, "(qemu)"); !ok {
		return fmt.Errorf("monitor prompt not seen")
	}
	if _, err := fmt.Fprintf(conn, "set_password vnc %s\n", pw); err != nil {
		return err
	}
	// Wait for the NEXT prompt so QEMU has actually executed the line before we close — the HMP
	// echoes input char-by-char, so reading only the first echo bytes and closing drops the command.
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	out, _ := readUntil(conn, "(qemu)")
	if strings.Contains(out, "Could not") {
		return fmt.Errorf("qemu: %s", strings.TrimSpace(out))
	}
	return nil
}

func readUntil(conn net.Conn, marker string) (string, bool) {
	var acc []byte
	buf := make([]byte, 1024)
	for {
		n, err := conn.Read(buf)
		acc = append(acc, buf[:n]...)
		if strings.Contains(string(acc), marker) {
			return string(acc), true
		}
		if err != nil {
			return string(acc), false
		}
	}
}

func (h *Handler) Create(cmd *cobra.Command, args []string) error {
	r, err := h.create(cmd, args[0])
	if err != nil {
		return err
	}
	fmt.Println(r.Name)
	return nil
}

func (h *Handler) Run(cmd *cobra.Command, args []string) error {
	r, err := h.create(cmd, args[0])
	if err != nil {
		return err
	}
	if err := h.launch(cmd, vmDir(cmd, r.Name), r); err != nil {
		return err
	}
	fmt.Printf("%s (pid %d)\n", r.Name, r.PID)
	return nil
}

func (h *Handler) Start(cmd *cobra.Command, args []string) error {
	for _, n := range args {
		dir := vmDir(cmd, n)
		r, err := loadRec(dir)
		if err != nil {
			return err
		}
		// reuse the persisted TAP/netns across stop/start (do not re-prepareNet); only rm tears down
		if err := h.launch(cmd, dir, r); err != nil {
			return err
		}
		fmt.Printf("%s (pid %d)\n", n, r.PID)
	}
	return nil
}

func (h *Handler) Stop(cmd *cobra.Command, args []string) error {
	grace := stopGracePeriod
	if force, _ := cmd.Flags().GetBool("force"); force {
		grace = 0 // immediate SIGKILL, skip the ACPI grace window
	}
	ctx := ctxOf(cmd)
	for _, n := range args {
		dir := vmDir(cmd, n)
		r, err := loadRec(dir)
		if err != nil {
			return err
		}
		terminate(ctx, r, grace)
		r.PID = 0
		_ = saveRec(dir, r)
		fmt.Println(n)
	}
	return nil
}

func (h *Handler) List(cmd *cobra.Command, args []string) error {
	ents, _ := os.ReadDir(filepath.Join(stateDir(cmd), "vms"))
	recs := []*record{}
	for _, e := range ents {
		if r, err := loadRec(filepath.Join(stateDir(cmd), "vms", e.Name())); err == nil {
			recs = append(recs, r)
		}
	}
	b, _ := json.MarshalIndent(recs, "", "  ")
	fmt.Println(string(b))
	return nil
}

func (h *Handler) Inspect(cmd *cobra.Command, args []string) error {
	r, err := loadRec(vmDir(cmd, args[0]))
	if err != nil {
		return err
	}
	b, _ := json.MarshalIndent(r, "", "  ")
	fmt.Println(string(b))
	return nil
}

func (h *Handler) Console(cmd *cobra.Command, args []string) error {
	r, err := loadRec(vmDir(cmd, args[0]))
	if err != nil {
		return err
	}
	fmt.Printf("VNC 127.0.0.1:590%d   SSH: ssh -p %d cocoon@localhost\n", r.VNCDisp, r.SSHPort)
	return nil
}

func (h *Handler) RM(cmd *cobra.Command, args []string) error {
	for _, n := range args {
		dir := vmDir(cmd, n)
		if r, err := loadRec(dir); err == nil {
			terminate(ctxOf(cmd), r, stopGracePeriod)
			teardownNet(cmd, r) // remove an auto-created TAP/netns (no-op for user --tap / user-mode)
		}
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
		fmt.Println(n)
	}
	return nil
}

// imagesToSnapshot lists the qcow2 images that make up a VM snapshot: the disk overlay always,
// plus OVMF_VARS only if it is qcow2 (raw .fd can't hold internal snapshots, so with a raw NVRAM
// the firmware vars do NOT roll back — only guest disk state does).
func imagesToSnapshot(r *record) []string {
	imgs := []string{r.Disk}
	if strings.HasSuffix(r.OVMFVars, ".qcow2") {
		imgs = append(imgs, r.OVMFVars)
	}
	return imgs
}

// Snapshot takes an offline qcow2-internal snapshot of a stopped VM (live snapshot would corrupt
// the image, and +invtsc blocks the live-migration codepath savevm relies on).
func (h *Handler) Snapshot(cmd *cobra.Command, args []string) error {
	dir := vmDir(cmd, args[0])
	r, err := loadRec(dir)
	if err != nil {
		return err
	}
	if running(r) {
		return fmt.Errorf("vm %q is running (pid %d); stop it first (qemu-img snapshot on a live image corrupts it)", r.Name, r.PID)
	}
	tag, _ := cmd.Flags().GetString("tag")
	if tag == "" {
		tag = "snap-" + time.Now().Format("20060102-150405")
	}
	ctx := ctxOf(cmd)
	for _, img := range imagesToSnapshot(r) {
		if err := qemu.SnapCreate(ctx, img, tag); err != nil {
			return err
		}
	}
	r.Snapshots = append(r.Snapshots, tag)
	if err := saveRec(dir, r); err != nil {
		return err
	}
	fmt.Println(tag)
	return nil
}

// Restore reverts a VM to a snapshot tag (default: newest). A running VM is refused unless --force,
// which stops it, reverts, and relaunches.
func (h *Handler) Restore(cmd *cobra.Command, args []string) error {
	dir := vmDir(cmd, args[0])
	r, err := loadRec(dir)
	if err != nil {
		return err
	}
	wasRunning := running(r)
	if wasRunning {
		if force, _ := cmd.Flags().GetBool("force"); !force {
			return fmt.Errorf("vm %q is running; stop it first or pass --force to stop+restore", r.Name)
		}
		terminate(ctxOf(cmd), r, stopGracePeriod)
		r.PID = 0
	}
	tag, _ := cmd.Flags().GetString("tag")
	if tag == "" {
		if len(r.Snapshots) == 0 {
			return fmt.Errorf("vm %q has no snapshots", r.Name)
		}
		tag = r.Snapshots[len(r.Snapshots)-1]
	}
	ctx := ctxOf(cmd)
	for _, img := range imagesToSnapshot(r) {
		if err := qemu.SnapApply(ctx, img, tag); err != nil {
			return err
		}
	}
	fmt.Println(tag)
	if wasRunning {
		return h.launch(cmd, dir, r)
	}
	return saveRec(dir, r)
}

// Clone seeds a new VM from SRC's disk state via a fresh CoW overlay on the SAME immutable base,
// with a unique Apple identity (cold boot re-runs OpenCore PlatformInfo) and its own TAP — so two
// clones never share a serial/MAC (App Store ban risk). Network/VNC/SSH come from flags so a clone
// doesn't collide on the source's host ports; CPUs/Memory/loader are inherited unless overridden.
func (h *Handler) Clone(cmd *cobra.Command, args []string) error {
	src := args[0]
	srcRec, err := loadRec(vmDir(cmd, src))
	if err != nil {
		return err
	}
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = src + "-clone-" + time.Now().Format("150405")
	}
	dir := vmDir(cmd, name)
	if err = os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	ctx := ctxOf(cmd)
	base, digest, err := resolveBase(cmd, srcRec.Image, name)
	if err != nil {
		return err
	}
	overlay := filepath.Join(dir, "disk.qcow2")
	if err = utils.RunQemuImg(ctx, "create", "-f", "qcow2", "-F", "qcow2", "-b", base, overlay); err != nil {
		return fmt.Errorf("bake clone overlay on %s: %w", base, err)
	}
	ovmfVars := filepath.Join(dir, filepath.Base(srcRec.OVMFVars))
	if err = copyFile(srcRec.OVMFVars, ovmfVars); err != nil {
		return fmt.Errorf("copy OVMF_VARS: %w", err)
	}
	r := &record{
		Name: name, Image: srcRec.Image, ImageDigest: digest, Disk: overlay,
		OVMFCode: srcRec.OVMFCode, OVMFVars: ovmfVars, CPUs: srcRec.CPUs, Memory: srcRec.Memory,
		VMID: newVMID(), Created: time.Now().Format(time.RFC3339),
	}
	if cmd.Flags().Changed("cpus") {
		r.CPUs, _ = cmd.Flags().GetInt("cpus")
	}
	if cmd.Flags().Changed("memory") {
		r.Memory, _ = cmd.Flags().GetString("memory")
	}
	r.Hugepages = srcRec.Hugepages
	if cmd.Flags().Changed("hugepages") {
		r.Hugepages, _ = cmd.Flags().GetBool("hugepages")
	}
	r.VNCDisp, _ = cmd.Flags().GetInt("vnc")
	r.SSHPort, _ = cmd.Flags().GetInt("ssh-port")
	r.VNCPass, _ = cmd.Flags().GetString("vnc-password")
	// fresh identity is mandatory when SRC has one; cold boot re-reads PlatformInfo from the overlay
	if random, _ := cmd.Flags().GetBool("random-smbios"); srcRec.SMBIOS != nil || random {
		if err = assignSMBIOS(ctx, dir, srcRec.OpenCore, r); err != nil {
			return err
		}
	} else {
		// no identity to vary: reuse SRC's loader verbatim (read-only via snapshot=on), no copy
		r.OpenCore, r.MAC = srcRec.OpenCore, srcRec.MAC
	}
	r.NetMode, _ = cmd.Flags().GetString("net")
	tapFlag, _ := cmd.Flags().GetString("tap")
	r.Tap = tapFlag
	netTap, netns, mac, err := prepareNet(cmd, r)
	if err != nil {
		return err
	}
	if r.MAC == "" {
		r.MAC = mac
	}
	if netTap != "" {
		r.Tap, r.Netns, r.TapOwned = netTap, netns, tapFlag == ""
	}
	if err := saveRec(dir, r); err != nil {
		return err
	}
	fmt.Println(name)
	return nil
}
