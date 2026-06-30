package vm

import (
	"context"
	"encoding/json"
	"errors"
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

	"github.com/cocoonstack/cocoon-macos/internal/home"
)

func loadRec(dir string) (*record, error) {
	var r record
	if err := utils.ReadJSONFile(filepath.Join(dir, "vm.json"), &r); err != nil {
		return nil, fmt.Errorf("read vm record: %w", err)
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

// bakeOverlay creates a per-VM CoW qcow2 overlay on the immutable base (which stays read-only).
func bakeOverlay(ctx context.Context, base, dst string) error {
	if err := utils.RunQemuImg(ctx, "create", "-f", "qcow2", "-F", "qcow2", "-b", base, dst); err != nil {
		return fmt.Errorf("bake overlay on %s: %w", base, err)
	}
	return nil
}

// applyNet provisions networking for r and records the result. userTap is the user's --tap value:
// an auto-created TAP is "owned" (torn down on rm) only when the user did not pass --tap.
func applyNet(cmd *cobra.Command, r *record, userTap string) error {
	netTap, netns, mac, err := prepareNet(cmd, r)
	if err != nil {
		return err
	}
	if r.MAC == "" {
		r.MAC = mac
	}
	if netTap != "" {
		r.Tap, r.Netns, r.TapOwned = netTap, netns, userTap == ""
	}
	return nil
}

// isRunning reports whether the VM's qemu process is alive AND is actually this VM's qemu (argv0 +
// the overlay path in /proc/<pid>/cmdline), so a recycled PID isn't mistaken for a live VM; falls
// back to a bare liveness probe on non-Linux.
func isRunning(r *record) bool {
	return utils.VerifyProcessCmdline(r.PID, qemuBinary, r.Disk)
}

// terminate stops the VM's qemu process (PID-reuse-safe — verifies the cmdline before signaling);
// grace=0 is the --force path (SIGTERM, then immediate SIGKILL).
func terminate(ctx context.Context, r *record, grace time.Duration) {
	if r.PID > 0 {
		_ = utils.TerminateProcess(ctx, r.PID, qemuBinary, r.Disk, grace)
	}
}

// graceFromFlags is the terminate grace period: 0 (immediate SIGKILL) when --force is set, else the
// default ACPI grace window.
func graceFromFlags(cmd *cobra.Command) time.Duration {
	if force, _ := cmd.Flags().GetBool("force"); force {
		return 0
	}
	return stopGracePeriod
}

// hostIsAMD reports whether the host CPU is AMD, which needs kvm.ignore_msrs=1 so macOS's reads of
// MSRs AMD lacks return 0 instead of #GP. Reads /proc/cpuinfo; false off Linux, where these VMs don't run.
func hostIsAMD() bool {
	b, err := os.ReadFile("/proc/cpuinfo")
	return err == nil && strings.Contains(string(b), "AuthenticAMD")
}

// resolveBase returns the immutable base qcow2 for image: a direct filesystem path (legacy), else
// an image ref resolved through cocoon's cloudimg store (returns the content-addressed blob + its
// digest). Per-VM overlays are baked on this base, which stays read-only.
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

// ensureCloudimgFirmware writes a placeholder CLOUDHV.fd where cocoon's cloudimg.Config insists a
// UEFI firmware exists (it targets cloud-hypervisor). cocoon-macos boots via OVMF + OpenCore and
// DISCARDS that BootConfig, so the file is never read — it only unblocks Config's validation.
func ensureCloudimgFirmware(cmd *cobra.Command) {
	fw := cloudimg.NewConfig(&config.Config{RootDir: home.Dir(cmd)}).FirmwarePath()
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
		return errors.New("monitor prompt not seen")
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

// flagOr returns the named string flag's value, or def when the flag is unset/empty.
func flagOr(cmd *cobra.Command, name, def string) string {
	if v, _ := cmd.Flags().GetString(name); v != "" {
		return v
	}
	return def
}
