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
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/cocoonstack/cocoon/config"
	"github.com/cocoonstack/cocoon/images/cloudimg"
	"github.com/cocoonstack/cocoon/types"
	"github.com/cocoonstack/cocoon/utils"
)

// stateDir is the root for all cocoon-macos state: --state-dir wins, else
// $COCOON_MACOS_HOME, else the default (mirrors cocoon's /var/lib/cocoon).
func stateDir(cmd *cobra.Command) string {
	if d, _ := cmd.Flags().GetString("state-dir"); d != "" {
		return d
	}
	if d := os.Getenv("COCOON_MACOS_HOME"); d != "" {
		return d
	}
	return defaultStateDir
}

// vmDir is the per-VM state directory <state-dir>/vms/<name>.
func vmDir(cmd *cobra.Command, name string) string {
	return filepath.Join(stateDir(cmd), "vms", name)
}

// firmwareDir is the managed home for shared loader/firmware assets (OpenCore base, OVMF_CODE,
// OVMF_VARS template) — populated by `cocoon-macos firmware install`, reused across all VMs.
func firmwareDir(cmd *cobra.Command) string {
	return filepath.Join(stateDir(cmd), "firmware")
}

// ctxOf returns the command's context, falling back to a fresh background
// context if cobra never set one (e.g. a unit test invoking the handler directly).
func ctxOf(cmd *cobra.Command) context.Context {
	if ctx := cmd.Context(); ctx != nil {
		return ctx
	}
	return context.Background()
}

func loadRec(dir string) (*record, error) {
	b, err := os.ReadFile(filepath.Join(dir, "vm.json"))
	if err != nil {
		return nil, fmt.Errorf("read vm record: %w", err)
	}
	var r record
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("parse vm record %s: %w", dir, err)
	}
	return &r, nil
}

func saveRec(dir string, r *record) error {
	b, _ := json.MarshalIndent(r, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "vm.json"), b, 0o600); err != nil {
		return fmt.Errorf("write vm record: %w", err)
	}
	return nil
}

func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("read %s: %w", src, err)
	}
	if err := os.WriteFile(dst, b, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", dst, err)
	}
	return nil
}

// newVMID is the network-plane id (cocoon truncates it to 8 chars via VMIDPrefix); random so
// same-second names don't collide.
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

// resolveBase returns the immutable base qcow2 for image: a direct filesystem path (legacy), else
// an image ref resolved through cocoon's cloudimg store (returns the content-addressed blob + its
// digest). Per-VM overlays are baked on this base, which stays read-only.
func resolveBase(cmd *cobra.Command, image, name string) (string, string, error) {
	if _, err := os.Stat(image); err == nil {
		return image, "", nil
	}
	ctx := ctxOf(cmd)
	root := stateDir(cmd)
	ensureCloudimgFirmware(root)
	store, err := cloudimg.New(ctx, &config.Config{RootDir: root, DNS: "8.8.8.8,1.1.1.1"})
	if err != nil {
		return "", "", fmt.Errorf("init cloudimg store: %w", err)
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
	if err := os.MkdirAll(filepath.Dir(fw), 0o750); err == nil {
		_ = os.WriteFile(fw, []byte("placeholder: cocoon-macos boots via OVMF, not CLOUDHV\n"), 0o600)
	}
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
		return fmt.Errorf("send set_password: %w", err)
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
