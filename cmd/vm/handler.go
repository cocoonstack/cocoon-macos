package vm

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/cocoonstack/cocoon-macos/qemu"
)

// Handler implements Actions by cloning a golden macOS qcow2 (copy-on-write overlay)
// and launching qemu-system-x86_64 on an x86 Linux/KVM host.
type Handler struct{}

func NewHandler() *Handler { return &Handler{} }

type record struct {
	Name     string       `json:"name"`
	Image    string       `json:"image"`
	Disk     string       `json:"disk"`
	OpenCore string       `json:"opencore"`
	OVMFCode string       `json:"ovmf_code"`
	OVMFVars string       `json:"ovmf_vars"`
	CPUs     int          `json:"cpus"`
	Memory   string       `json:"memory"`
	VNCDisp  int          `json:"vnc"`
	SSHPort  int          `json:"ssh_port"`
	PID      int          `json:"pid"`
	Created  string       `json:"created"`
	MAC      string       `json:"mac,omitempty"`
	SMBIOS   *qemu.SMBIOS `json:"smbios,omitempty"`
	VNCPass  string       `json:"vnc_password,omitempty"`
}

func stateDir(cmd *cobra.Command) string {
	if d, _ := cmd.Flags().GetString("state-dir"); d != "" {
		return d
	}
	if d := os.Getenv("COCOON_MACOS_HOME"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cocoon-macos")
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

// create derives a per-VM copy-on-write overlay on the golden image + writes the record.
func (h *Handler) create(cmd *cobra.Command, image string) (*record, error) {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "macos-" + time.Now().Format("20060102-150405")
	}
	oc, _ := cmd.Flags().GetString("opencore")
	code, _ := cmd.Flags().GetString("ovmf-code")
	varsTmpl, _ := cmd.Flags().GetString("ovmf-vars")
	if oc == "" || code == "" || varsTmpl == "" {
		return nil, fmt.Errorf("--opencore, --ovmf-code and --ovmf-vars are required")
	}
	dir := vmDir(cmd, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	overlay := filepath.Join(dir, "disk.qcow2")
	if out, err := exec.Command("qemu-img", "create", "-f", "qcow2", "-b", image, "-F", "qcow2", overlay).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("qemu-img create overlay: %v: %s", err, out)
	}
	ovmfVars := filepath.Join(dir, "OVMF_VARS.fd")
	if err := copyFile(varsTmpl, ovmfVars); err != nil {
		return nil, fmt.Errorf("copy OVMF_VARS: %w", err)
	}
	cpus, _ := cmd.Flags().GetInt("cpus")
	mem, _ := cmd.Flags().GetString("memory")
	vnc, _ := cmd.Flags().GetInt("vnc")
	ssh, _ := cmd.Flags().GetInt("ssh-port")
	vncPass, _ := cmd.Flags().GetString("vnc-password")
	r := &record{
		Name: name, Image: image, Disk: overlay, OpenCore: oc, OVMFCode: code, OVMFVars: ovmfVars,
		CPUs: cpus, Memory: mem, VNCDisp: vnc, SSHPort: ssh, VNCPass: vncPass, Created: time.Now().Format(time.RFC3339),
	}
	if random, _ := cmd.Flags().GetBool("random-smbios"); random {
		if err := assignSMBIOS(dir, oc, r); err != nil {
			return nil, err
		}
	}
	return r, saveRec(dir, r)
}

// assignSMBIOS gives the VM a unique identity: a per-VM OpenCore copy with PlatformInfo/Generic
// injected, so clones do not all boot as the shipped placeholder serial.
func assignSMBIOS(dir, oc string, r *record) error {
	sm, err := qemu.RandomSMBIOS()
	if err != nil {
		return err
	}
	ocCopy := filepath.Join(dir, "OpenCore.qcow2")
	if err := copyFile(oc, ocCopy); err != nil {
		return fmt.Errorf("copy OpenCore: %w", err)
	}
	if err := qemu.InjectSMBIOS(ocCopy, sm); err != nil {
		return fmt.Errorf("inject SMBIOS: %w", err)
	}
	r.OpenCore, r.SMBIOS, r.MAC = ocCopy, &sm, sm.MAC()
	return nil
}

func (h *Handler) launch(dir string, r *record) error {
	spec := qemu.Spec{
		Name: r.Name, Disk: r.Disk, OpenCore: r.OpenCore, OVMFCode: r.OVMFCode, OVMFVars: r.OVMFVars,
		CPUs: r.CPUs, Memory: r.Memory, VNCDisp: r.VNCDisp, SSHPort: r.SSHPort, MAC: r.MAC, VNCPass: r.VNCPass,
		MonSock: filepath.Join(dir, "monitor.sock"), QMPSock: filepath.Join(dir, "qmp.sock"),
	}
	pidfile := filepath.Join(dir, "qemu.pid")
	args := append(spec.Args(), "-daemonize", "-pidfile", pidfile)
	c := exec.Command("qemu-system-x86_64", args...)
	c.Stdout, c.Stderr = os.Stdout, os.Stderr
	if err := c.Run(); err != nil {
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
	if _, err := fmt.Fprintf(conn, "set_password vnc %s\n", pw); err != nil {
		return err
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 4096)
	n, _ := conn.Read(buf)
	if strings.Contains(string(buf[:n]), "Could not") {
		return fmt.Errorf("qemu: %s", strings.TrimSpace(string(buf[:n])))
	}
	return nil
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
	if err := h.launch(vmDir(cmd, r.Name), r); err != nil {
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
		if err := h.launch(dir, r); err != nil {
			return err
		}
		fmt.Printf("%s (pid %d)\n", n, r.PID)
	}
	return nil
}

func (h *Handler) Stop(cmd *cobra.Command, args []string) error {
	for _, n := range args {
		r, err := loadRec(vmDir(cmd, n))
		if err != nil {
			return err
		}
		if r.PID > 0 {
			_ = exec.Command("kill", strconv.Itoa(r.PID)).Run()
		}
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
		if r, err := loadRec(dir); err == nil && r.PID > 0 {
			_ = exec.Command("kill", strconv.Itoa(r.PID)).Run()
		}
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
		fmt.Println(n)
	}
	return nil
}
