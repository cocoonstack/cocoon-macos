package vm

import (
	"context"
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

// vncProxyCommand is the hidden re-exec target that runs the forwarder for its lifetime.
func vncProxyCommand() *cobra.Command {
	return &cobra.Command{
		Use: vncProxyOp + " <listen> <unix-sock>", Hidden: true, Args: cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error { return runVNCProxy(args[0], args[1]) },
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
	self, err := os.Executable()
	if err != nil {
		return err
	}
	listen := fmt.Sprintf("0.0.0.0:%d", vncBasePort+disp)
	c := exec.Command(self, "vm", vncProxyOp, listen, sock)
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // survive the parent CLI exit
	if err := c.Start(); err != nil {
		return err
	}
	return utils.WritePIDFile(filepath.Join(dir, vncProxyPID), c.Process.Pid)
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

func runVNCProxy(listen, sock string) error {
	l, err := net.Listen("tcp", listen)
	if err != nil {
		return fmt.Errorf("listen %s: %w", listen, err)
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
