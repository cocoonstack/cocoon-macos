package vm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/cocoonstack/cocoon/utils"
)

// vncSockName / vncProxyPID live in the per-VM dir. The proxy fronts the netns-local QEMU VNC unix
// socket (see qemu.Spec.VNCSock) on a host TCP port so a CNI VM's console is reachable off-box.
const (
	vncSockName = "vnc.sock"
	vncProxyPID = "vnc-proxy.pid"
	vncProxyOp  = "_vnc-proxy"
	vncBasePort = 5900
)

// Unlike the loopback bind of the other net modes, the CNI proxy listens on all host interfaces —
// never expose that unauthenticated.
var errCNIVNCPassRequired = errors.New("--vnc with --net cni serves VNC on a host port reachable off-box; --vnc-password is required")

// requireCNIVNCPassword rejects an unauthenticated VNC display on a CNI VM whose
// host port is reachable off-box. isCNI is the flag intent at create/clone
// (pre-scaffold) or the resolved Netns at launch (post-scaffold).
func requireCNIVNCPassword(isCNI bool, vncDisp int, vncPass string) error {
	if isCNI && vncDisp >= 0 && vncPass == "" {
		return errCNIVNCPassRequired
	}
	return nil
}

// vncProxyCommand is the hidden re-exec target that runs the forwarder for its lifetime, accepting
// on the TCP listener inherited as fd 3 (bound by the parent so bind errors fail the launch).
func vncProxyCommand() *cobra.Command {
	return &cobra.Command{
		Use: vncProxyOp + " <unix-sock>", Hidden: true, Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error { return runVNCProxy(args[0]) },
	}
}

// startVNCProxy re-execs this binary as the hidden proxy, detached, in the HOST netns (so its TCP
// listener is reachable off-box while qemu's VNC stays inside the CNI netns). Idempotent.
func startVNCProxy(ctx context.Context, dir string, disp int) error {
	stopVNCProxy(ctx, dir) // a stale proxy would hold the port and shadow the new one
	sock := filepath.Join(dir, vncSockName)
	if err := utils.WaitFor(ctx, 5*time.Second, 100*time.Millisecond, func() (bool, error) {
		info, statErr := os.Stat(sock) // qemu -daemonize creates the socket slightly after fork
		return statErr == nil && info.Mode()&os.ModeSocket != 0, nil
	}); err != nil {
		return fmt.Errorf("vnc socket %s not created by qemu: %w", sock, err)
	}
	// Bind here, not in the child: the child is detached, so its bind error (port taken) would be
	// invisible and the launch would report a VNC entry point that doesn't exist.
	l, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", vncBasePort+disp))
	if err != nil {
		return fmt.Errorf("bind vnc port: %w", err)
	}
	defer func() { _ = l.Close() }() // child holds its own dup
	lf, err := l.(*net.TCPListener).File()
	if err != nil {
		return err
	}
	defer func() { _ = lf.Close() }()
	self, err := os.Executable()
	if err != nil {
		return err
	}
	c := exec.Command(self, "vm", vncProxyOp, sock)
	c.ExtraFiles = []*os.File{lf}                      // inherited as fd 3
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // survive the parent CLI exit
	if err := c.Start(); err != nil {
		return err
	}
	if err := utils.WritePIDFile(filepath.Join(dir, vncProxyPID), c.Process.Pid); err != nil {
		_ = c.Process.Kill() // no pidfile -> stopVNCProxy can't reap it; don't orphan the proxy
		return err
	}
	return nil
}

// stopVNCProxy kills a running proxy (best-effort) and removes its pidfile. Zero grace: the CLI's
// root command traps SIGTERM via signal.NotifyContext, so a TERMed proxy would just keep accepting;
// TerminateProcess then escalates straight to SIGKILL, which loses nothing on a stateless pipe.
func stopVNCProxy(ctx context.Context, dir string) {
	pidPath := filepath.Join(dir, vncProxyPID)
	if pid, err := utils.ReadPIDFile(pidPath); err == nil {
		_ = utils.TerminateProcess(ctx, pid, filepath.Base(os.Args[0]), vncProxyOp, 0)
	}
	_ = os.Remove(pidPath)
}

func runVNCProxy(sock string) error {
	l, err := net.FileListener(os.NewFile(3, "vnc-listener"))
	if err != nil {
		return fmt.Errorf("inherit vnc listener: %w", err)
	}
	defer func() { _ = l.Close() }()
	for {
		client, err := l.Accept()
		if err != nil {
			return err
		}
		go pipeVNC(client, sock)
	}
}

// pipeVNC bridges one client connection to the VM's VNC unix socket, copying both directions.
func pipeVNC(client net.Conn, sock string) {
	defer func() { _ = client.Close() }()
	backend, err := net.Dial("unix", sock)
	if err != nil {
		return
	}
	defer func() { _ = backend.Close() }()
	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn) { _, _ = io.Copy(dst, src); done <- struct{}{} }
	go cp(backend, client)
	go cp(client, backend)
	<-done
}
