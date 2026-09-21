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

	"github.com/projecteru2/core/log"
	"github.com/spf13/cobra"

	"github.com/cocoonstack/cocoon-macos/home"
	"github.com/cocoonstack/cocoon-macos/internal/procutil"
	"github.com/cocoonstack/cocoon/cmd/cliutil"
	"github.com/cocoonstack/cocoon/images/cloudimg"
	"github.com/cocoonstack/cocoon/lock/flock"
	"github.com/cocoonstack/cocoon/types"
	"github.com/cocoonstack/cocoon/utils"
)

const (
	vmCleanupTimeout = 30 * time.Second
	hmpPrompt        = "(qemu) "
	monitorSockName  = "monitor.sock"
)

func loadRec(dir string) (*record, error) {
	r := new(record)
	if err := utils.ReadJSONFile(filepath.Join(dir, "vm.json"), r); err != nil {
		return nil, fmt.Errorf("read vm record: %w", err)
	}
	return r, nil
}

func saveRec(dir string, r *record) error {
	if err := utils.AtomicWriteJSON(filepath.Join(dir, "vm.json"), r, utils.Sync); err != nil {
		return fmt.Errorf("write vm record: %w", err)
	}
	return nil
}

// lock lives outside the VM dir so rm can't unlink the inode a waiter still holds and split mutual exclusion.
func withVMLock(ctx context.Context, dir string, fn func() error) error {
	lockPath := vmLockPath(dir)
	if err := utils.EnsureDirs(filepath.Dir(lockPath)); err != nil {
		return fmt.Errorf("create vm lock dir: %w", err)
	}
	l := flock.NewTransient(lockPath)
	if err := l.Lock(ctx); err != nil {
		return fmt.Errorf("lock vm: %w", err)
	}
	defer func() { _ = l.Unlock(context.WithoutCancel(ctx)) }()
	return fn()
}

func vmLockPath(dir string) string {
	return filepath.Join(filepath.Dir(dir), ".locks", filepath.Base(dir)+".lock")
}

func forEachVMDir(cmd *cobra.Command, args []string, fn func(ctx context.Context, name, dir string) error) error {
	ctx := cliutil.CommandContext(cmd)
	for _, n := range args {
		dir, err := home.VMDir(cmd, n)
		if err != nil {
			return err
		}
		if err := withVMLock(ctx, dir, func() error { return fn(ctx, n, dir) }); err != nil {
			return err
		}
	}
	return nil
}

// withVMLocks takes both locks in path order, so two clones crossing the same pair never deadlock.
func withVMLocks(ctx context.Context, a, b string, fn func() error) error {
	if a == b {
		return withVMLock(ctx, a, fn)
	}
	if a > b {
		a, b = b, a
	}
	return withVMLock(ctx, a, func() error { return withVMLock(ctx, b, fn) })
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
	n, err := types.ParseSize(raw)
	if err == nil && n <= 0 {
		err = errors.New("size must be positive")
	}
	if err != nil {
		return 0, fmt.Errorf("invalid --storage %q: %w", raw, err)
	}
	return n, nil
}

// resizeSystemDisk grows a new overlay to target bytes (0 = keep image size); shrinking is rejected because qemu-img cannot prove the guest filesystem survives.
func resizeSystemDisk(ctx context.Context, path string, target int64) (int64, error) {
	hdr, _, err := utils.ReadQcow2Header(path)
	if err != nil {
		return 0, fmt.Errorf("read system disk %s: %w", path, err)
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
func scaffoldVM(cmd *cobra.Command, name, image, varsSrc string) (dir, overlay, ovmfVars, digest string, err error) {
	dir, err = home.VMDir(cmd, name)
	if err != nil {
		return "", "", "", "", err
	}
	monitor := filepath.Join(dir, monitorSockName)
	if len(monitor) > 107 {
		return "", "", "", "", fmt.Errorf("monitor socket path is %d bytes; shorten --state-dir or --name to fit Linux's 107-byte limit", len(monitor))
	}
	if _, statErr := os.Stat(filepath.Join(dir, "vm.json")); statErr == nil {
		return "", "", "", "", fmt.Errorf("vm %q already exists; rm it first or pick another --name", name)
	} else if !os.IsNotExist(statErr) {
		return "", "", "", "", fmt.Errorf("stat vm record: %w", statErr)
	}
	ctx := cliutil.CommandContext(cmd)
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), vmCleanupTimeout)
	defer cancel()
	if err = resetIncompleteVMDir(cleanupCtx, dir); err != nil {
		return "", "", "", "", err
	}
	base, digest, err := resolveBase(ctx, cmd, image, name)
	if err != nil {
		return "", "", "", "", err
	}
	if err = utils.EnsureDirs(dir); err != nil {
		return "", "", "", "", err
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(dir)
		}
	}()
	overlay = filepath.Join(dir, "disk.qcow2")
	if err = bakeOverlay(ctx, base, overlay); err != nil {
		return "", "", "", "", err
	}
	ovmfVars = filepath.Join(dir, filepath.Base(varsSrc))
	if err = utils.ReflinkCopy(ctx, ovmfVars, varsSrc, utils.Sync); err != nil {
		return "", "", "", "", fmt.Errorf("copy OVMF_VARS: %w", err)
	}
	return dir, overlay, ovmfVars, digest, nil
}

func resetIncompleteVMDir(ctx context.Context, dir string) error {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return fmt.Errorf("stat incomplete vm dir: %w", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "vm.json")); err == nil {
		return fmt.Errorf("refuse to remove committed vm dir %s", dir)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat vm record: %w", err)
	}
	if pids, err := utils.FindVMMByCmdline(qemuBinary, vmDirPrefix(dir)); err != nil {
		return fmt.Errorf("scan qemu processes for %s: %w", dir, err)
	} else if len(pids) > 0 {
		return fmt.Errorf("refuse to replace incomplete vm dir %s: live qemu pids %v", dir, pids)
	}
	if err := procutil.TerminateByCmdline(ctx, "qemu-nbd", vmDirPrefix(dir), time.Second); err != nil {
		return fmt.Errorf("cleanup stale qemu-nbd for %s: %w", dir, err)
	}
	if err := markProvisioned(filepath.Dir(dir)); err != nil {
		return err
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("remove incomplete vm dir %s: %w", dir, err)
	}
	return nil
}

// vmDirPrefix is the cmdline needle for a VM dir: the separator keeps foo from matching foo-clone.
func vmDirPrefix(dir string) string { return dir + "/" }

func validateTapFlag(netMode, tap string) error {
	if tap != "" && netMode != netTAP {
		return fmt.Errorf("--tap requires --net tap, got --net %s", cmp.Or(netMode, netUser))
	}
	return nil
}

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

func applyNet(cmd *cobra.Command, r *record) error {
	if err := markProvisioned(home.VMsDir(cmd)); err != nil {
		return err
	}
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

func isRunning(r *record) bool {
	return utils.VerifyProcessCmdline(r.PID, qemuBinary, qemuPIDPath(r.Disk))
}

// reconcileRunningQEMU adopts a qemu that daemonized before its pid was saved; >1 match is corruption, not a guess.
func reconcileRunningQEMU(dir string, r *record) (bool, error) {
	if isRunning(r) {
		return true, nil
	}
	pids, err := utils.FindVMMByCmdline(qemuBinary, qemuPIDPath(r.Disk))
	if err != nil {
		return false, fmt.Errorf("scan qemu process for %s: %w", r.Disk, err)
	}
	switch len(pids) {
	case 0:
		return false, nil
	case 1:
		r.PID = pids[0]
	default:
		return false, fmt.Errorf("multiple qemu processes use disk %s: %v", r.Disk, pids)
	}
	if err := saveRec(dir, r); err != nil {
		return false, err
	}
	return true, nil
}

func terminate(ctx context.Context, r *record, grace time.Duration) error {
	if r.PID <= 0 {
		return nil
	}
	if err := utils.TerminateProcess(ctx, r.PID, qemuBinary, qemuPIDPath(r.Disk), grace); err != nil {
		return fmt.Errorf("terminate qemu pid %d: %w", r.PID, err)
	}
	return nil
}

func qemuPIDPath(disk string) string {
	return filepath.Join(filepath.Dir(disk), "qemu.pid")
}

func stopInstance(ctx context.Context, dir string, r *record, grace time.Duration) error {
	if grace > 0 && isRunning(r) && powerDown(ctx, filepath.Join(dir, monitorSockName), r, grace) {
		grace = 0
	}
	err := terminate(ctx, r, grace)
	stopVNCProxy(ctx, dir)
	return err
}

func graceFromFlags(cmd *cobra.Command) time.Duration {
	if force, _ := cmd.Flags().GetBool("force"); force {
		return 0
	}
	return stopGracePeriod
}

func isHostAMD() bool {
	b, err := os.ReadFile("/proc/cpuinfo")
	return err == nil && strings.Contains(string(b), "AuthenticAMD")
}

// resolveBase returns the immutable base qcow2 (+ digest): a direct filesystem path, else an image ref resolved to its content-addressed blob in cocoon's cloudimg store.
func resolveBase(ctx context.Context, cmd *cobra.Command, image, name string) (string, string, error) {
	if utils.FileExists(image) {
		return image, "", nil
	}
	store, err := home.OpenStore(ctx, cmd)
	if err != nil {
		return "", "", err
	}
	img, err := store.Inspect(ctx, image)
	if err != nil {
		return "", "", fmt.Errorf("resolve image %q for vm %s: %w", image, name, err)
	}
	if img == nil {
		return "", "", fmt.Errorf("resolve image %q for vm %s: not a file, not in the store", image, name)
	}
	hex, _ := strings.CutPrefix(img.ID, "sha256:")
	blob := cloudimg.NewConfig(home.Dir(cmd), 0).BlobPath(hex)
	if !utils.ValidFile(blob) {
		return "", "", fmt.Errorf("blob %s invalid for vm %s (image %q)", img.ID, name, image)
	}
	return blob, img.ID, nil
}

func resolveFirmware(cmd *cobra.Command) (opencore, code, vars string, err error) {
	fw := home.FirmwareDir(cmd)
	opencore = flagOr(cmd, "opencore", filepath.Join(fw, "OpenCore.qcow2"))
	code = flagOr(cmd, "ovmf-code", filepath.Join(fw, "OVMF_CODE.fd"))
	vars = flagOr(cmd, "ovmf-vars", filepath.Join(fw, "OVMF_VARS.fd"))
	for _, p := range []*string{&opencore, &code, &vars} {
		if !utils.ValidFile(*p) {
			return "", "", "", fmt.Errorf("firmware not found: %s — run scripts/doctor.sh to provision it (or pass --opencore/--ovmf-code/--ovmf-vars)", *p)
		}
		if *p, err = filepath.Abs(*p); err != nil {
			return "", "", "", fmt.Errorf("resolve firmware path: %w", err)
		}
	}
	return opencore, code, vars, nil
}

// powerDown asks the guest to shut down over the monitor and waits up to grace for qemu to exit; false means the request never reached qemu, so the caller keeps its grace for SIGTERM.
func powerDown(ctx context.Context, monSock string, r *record, grace time.Duration) bool {
	logger := log.WithFunc("cmd.vm.powerDown")
	out, err := hmpCommand(ctx, monSock, "system_powerdown")
	if err == nil && hmpReplied(out, "system_powerdown") {
		err = fmt.Errorf("qemu rejected system_powerdown: %s", strings.TrimSpace(out))
	}
	if err != nil {
		logger.Warnf(ctx, "acpi power-down for %s: %v", r.Name, err)
		return false
	}
	if err := utils.WaitFor(ctx, grace, 200*time.Millisecond, func() (bool, error) { return !isRunning(r), nil }); err != nil {
		logger.Infof(ctx, "guest %s did not halt within %s; signaling qemu", r.Name, grace)
	}
	return true
}

// setVNCPassword applies the VNC password over the HMP monitor (QEMU was started with password=on).
func setVNCPassword(ctx context.Context, monSock, pw string) error {
	out, err := hmpCommand(ctx, monSock, "set_password vnc "+pw)
	if err != nil {
		return err
	}
	if hmpReplied(out, "set_password ") {
		// HMP reports failure only by printing; out echoes the typed password, so never surface it
		return errors.New("qemu rejected set_password (vnc display not active?)")
	}
	return nil
}

// hmpCommand runs one HMP line and returns everything the monitor printed up to its next prompt.
func hmpCommand(ctx context.Context, monSock, line string) (string, error) {
	var conn net.Conn
	var dialErr error
	// the monitor socket appears asynchronously after -daemonize
	if err := utils.WaitFor(ctx, 5*time.Second, 100*time.Millisecond, func() (bool, error) {
		conn, dialErr = net.Dial("unix", monSock)
		return dialErr == nil, nil
	}); err != nil {
		return "", fmt.Errorf("dial monitor: %w", cmp.Or(dialErr, err))
	}
	defer func() { _ = conn.Close() }()
	// the monitor discards input until its first prompt, so an early command is silently lost
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, ok := readUntil(conn, hmpPrompt); !ok {
		return "", errors.New("monitor prompt not seen")
	}
	verb, _, _ := strings.Cut(line, " ")
	if _, err := fmt.Fprintf(conn, "%s\n", line); err != nil {
		return "", fmt.Errorf("send %s: %w", verb, err)
	}
	// wait for the next prompt so QEMU has executed the line before we close (HMP echoes char-by-char)
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	out, ok := readUntil(conn, hmpPrompt)
	if !ok {
		return "", fmt.Errorf("monitor closed before answering %s", verb)
	}
	return out, nil
}

// hmpReplied reports whether the monitor printed anything besides echoing the command whose text contains echo.
func hmpReplied(out, echo string) bool {
	echoed := false
	for line := range strings.SplitSeq(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, strings.TrimSpace(hmpPrompt)) {
			continue
		}
		if !echoed && strings.Contains(line, echo) {
			echoed = true
			continue
		}
		return true
	}
	return false
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

func inherit[T any](cmd *cobra.Command, flag string, base T, get func(string) (T, error)) T {
	if !cmd.Flags().Changed(flag) {
		return base
	}
	v, _ := get(flag)
	return v
}
