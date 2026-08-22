package vm

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/docker/go-units"
	"github.com/spf13/cobra"

	"github.com/cocoonstack/cocoon-macos/home"
	"github.com/cocoonstack/cocoon/cmd/cliutil"
	"github.com/cocoonstack/cocoon/images"
	"github.com/cocoonstack/cocoon/lock/flock"
	"github.com/cocoonstack/cocoon/types"
	"github.com/cocoonstack/cocoon/utils"
)

func loadRec(dir string) (*record, error) {
	var r record
	if err := utils.ReadJSONFile(filepath.Join(dir, "vm.json"), &r); err != nil {
		return nil, fmt.Errorf("read vm record: %w", err)
	}
	return &r, nil
}

// saveRec writes vm.json atomically (temp + fsync + rename) so a crash can't truncate it.
func saveRec(dir string, r *record) error {
	if err := utils.AtomicWriteJSON(filepath.Join(dir, "vm.json"), r); err != nil {
		return fmt.Errorf("write vm record: %w", err)
	}
	return nil
}

// withVMLock serializes concurrent lifecycle ops on one VM (vm.json is read-modify-write).
func withVMLock(ctx context.Context, dir string, fn func() error) error {
	l := flock.New(filepath.Join(dir, "vm.json.lock"))
	if err := l.Lock(ctx); err != nil {
		return fmt.Errorf("lock vm: %w", err)
	}
	defer func() { _ = l.Unlock(ctx) }()
	return fn()
}

// bakeOverlay creates a per-VM CoW qcow2 overlay on the immutable base (which stays read-only).
func bakeOverlay(ctx context.Context, base, dst string) error {
	if err := utils.RunQemuImg(ctx, "create", "-f", "qcow2", "-F", "qcow2", "-b", base, dst); err != nil {
		return fmt.Errorf("bake overlay on %s: %w", base, err)
	}
	return nil
}

func storageFromFlag(cmd *cobra.Command) (int64, error) {
	raw, _ := cmd.Flags().GetString("storage")
	if strings.TrimSpace(raw) == "" {
		return 0, nil
	}
	parsed := strings.TrimSpace(raw)
	if strings.HasSuffix(strings.ToLower(parsed), "i") {
		parsed = parsed[:len(parsed)-1]
	}
	n, err := units.RAMInBytes(parsed)
	if err != nil {
		return 0, fmt.Errorf("invalid --storage %q: %w", raw, err)
	}
	if n <= 0 {
		return 0, fmt.Errorf("invalid --storage %q: size must be positive", raw)
	}
	return n, nil
}

// resizeSystemDisk expands a newly-created overlay to target bytes. A zero
// target preserves the image size; shrinking is rejected because qemu-img
// cannot prove that the guest partition/filesystem would remain intact.
func resizeSystemDisk(ctx context.Context, path string, target int64) (int64, error) {
	hdr, ok, err := utils.ReadQcow2Header(path)
	if err != nil {
		return 0, fmt.Errorf("read system disk %s: %w", path, err)
	}
	if !ok {
		return 0, fmt.Errorf("system disk %s is not qcow2", path)
	}
	if target == 0 || target == hdr.VirtualSize {
		return hdr.VirtualSize, nil
	}
	if target < hdr.VirtualSize {
		return 0, fmt.Errorf("--storage %d bytes is smaller than image virtual size %d bytes; shrinking is not supported", target, hdr.VirtualSize)
	}
	if err := utils.RunQemuImg(ctx, "resize", path, fmt.Sprintf("%d", target)); err != nil {
		return 0, fmt.Errorf("resize system disk %s to %d bytes: %w", path, target, err)
	}
	return target, nil
}

// scaffoldVM lays down a new VM dir, disk overlay, and OVMF_VARS copy; it refuses an existing record — a second create/clone under the same name would truncate the live overlay.
func scaffoldVM(cmd *cobra.Command, name, image, varsSrc, varsName string) (dir, overlay, ovmfVars, digest string, err error) {
	dir = home.VMDir(cmd, name)
	if _, statErr := os.Stat(filepath.Join(dir, "vm.json")); statErr == nil {
		return "", "", "", "", fmt.Errorf("vm %q already exists; rm it first or pick another --name", name)
	}
	if err = utils.EnsureDirs(dir); err != nil {
		return "", "", "", "", err
	}
	base, digest, err := resolveBase(cmd, image, name)
	if err != nil {
		return "", "", "", "", err
	}
	overlay = filepath.Join(dir, "disk.qcow2")
	if err = bakeOverlay(cliutil.CommandContext(cmd), base, overlay); err != nil {
		return "", "", "", "", err
	}
	ovmfVars = filepath.Join(dir, varsName)
	if err = utils.ReflinkCopy(ovmfVars, varsSrc, utils.Sync); err != nil {
		return "", "", "", "", fmt.Errorf("copy OVMF_VARS: %w", err)
	}
	return dir, overlay, ovmfVars, digest, nil
}

// prepareNet returns the TAP ifname, netns path (CNI only), and guest MAC; user-mode and a pre-created --tap need no provisioning, every other mode goes through the per-OS provisionNet.
func prepareNet(cmd *cobra.Command, r *record) (tap, netns, mac string, err error) {
	switch r.NetMode {
	case "", netUser:
		return "", "", r.MAC, nil
	case netTAP:
		if r.Tap != "" { // user pre-created the TAP (already on a bridge / cocoon CNI) — use verbatim
			return r.Tap, "", r.MAC, nil
		}
	}
	return provisionNet(cmd, r)
}

// applyNet provisions networking and records it; a TAP is "owned" (torn down on rm) only when auto-created, never when the user passed --tap.
func applyNet(cmd *cobra.Command, r *record) error {
	userTap := r.Tap
	netTap, netns, mac, err := prepareNet(cmd, r)
	if err != nil {
		return err
	}
	r.MAC = mac
	if netTap != "" {
		r.Tap, r.Netns, r.TapOwned = netTap, netns, userTap == ""
	}
	return nil
}

// isRunning is PID-reuse-safe: it verifies argv0 + the overlay path in the process cmdline.
func isRunning(r *record) bool {
	return utils.VerifyProcessCmdline(r.PID, qemuBinary, r.Disk)
}

// terminate stops the VM's qemu, verifying the cmdline before signaling; grace=0 means immediate SIGKILL.
func terminate(ctx context.Context, r *record, grace time.Duration) {
	if r.PID > 0 {
		_ = utils.TerminateProcess(ctx, r.PID, qemuBinary, r.Disk, grace)
	}
}

func graceFromFlags(cmd *cobra.Command) time.Duration {
	if force, _ := cmd.Flags().GetBool("force"); force {
		return 0
	}
	return stopGracePeriod
}

func hostIsAMD() bool {
	b, err := os.ReadFile("/proc/cpuinfo")
	return err == nil && strings.Contains(string(b), "AuthenticAMD")
}

// resolveBase returns the immutable base qcow2 (+ digest): a direct filesystem path, else an image ref resolved through cocoon's cloudimg store.
func resolveBase(cmd *cobra.Command, image, name string) (string, string, error) {
	if _, err := os.Stat(image); err == nil {
		return image, "", nil
	}
	ensureCloudimgFirmware(cmd)
	ctx, store, err := home.OpenStore(cmd)
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

// ensureCloudimgFirmware writes a placeholder CLOUDHV.fd purely to satisfy cloudimg.Config's firmware validation — cocoon-macos boots via OVMF and never reads it.
func ensureCloudimgFirmware(cmd *cobra.Command) {
	fw := images.FirmwarePath(home.Dir(cmd))
	if utils.ValidFile(fw) {
		return
	}
	if err := os.MkdirAll(filepath.Dir(fw), 0o750); err == nil {
		_ = os.WriteFile(fw, []byte("placeholder: cocoon-macos boots via OVMF, not CLOUDHV\n"), 0o600)
	}
}

// resolveFirmware returns the OpenCore loader + OVMF code/vars base/template paths: an explicit flag wins, else the shared copy under <state-dir>/firmware/.
func resolveFirmware(cmd *cobra.Command) (opencore, code, vars string, err error) {
	fw := home.FirmwareDir(cmd)
	opencore = flagOr(cmd, "opencore", filepath.Join(fw, "OpenCore.qcow2"))
	code = flagOr(cmd, "ovmf-code", filepath.Join(fw, "OVMF_CODE.fd"))
	vars = flagOr(cmd, "ovmf-vars", filepath.Join(fw, "OVMF_VARS.fd"))
	for _, p := range []string{opencore, code, vars} {
		if !utils.ValidFile(p) {
			return "", "", "", fmt.Errorf("firmware not found: %s — run scripts/doctor.sh to provision it (or pass --opencore/--ovmf-code/--ovmf-vars)", p)
		}
	}
	return opencore, code, vars, nil
}

// setVNCPassword applies the VNC password over the HMP monitor (QEMU was started with password=on).
func setVNCPassword(ctx context.Context, monSock, pw string) error {
	// Start reaches here without the create/clone pre-check
	if err := validateVNCPassword(pw); err != nil {
		return err
	}
	var conn net.Conn
	var dialErr error
	// the monitor socket appears asynchronously after -daemonize
	if err := utils.WaitFor(ctx, 5*time.Second, 100*time.Millisecond, func() (bool, error) {
		conn, dialErr = net.Dial("unix", monSock)
		return dialErr == nil, nil
	}); err != nil {
		return fmt.Errorf("dial monitor: %w", cmp.Or(dialErr, err))
	}
	defer func() { _ = conn.Close() }()
	// the monitor discards input until its first prompt, so an early set_password is silently lost
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, ok := readUntil(conn, "(qemu)"); !ok {
		return errors.New("monitor prompt not seen")
	}
	if _, err := fmt.Fprintf(conn, "set_password vnc %s\n", pw); err != nil {
		return fmt.Errorf("send set_password: %w", err)
	}
	// wait for the next prompt so QEMU has executed the line before we close (HMP echoes char-by-char)
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	out, _ := readUntil(conn, "(qemu)")
	if strings.Contains(out, "Could not") {
		// out echoes the typed "set_password vnc <pw>" line — never surface it
		return errors.New("qemu rejected set_password (vnc display not active?)")
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

func flagOr(cmd *cobra.Command, name, def string) string {
	v, _ := cmd.Flags().GetString(name)
	return cmp.Or(v, def)
}
