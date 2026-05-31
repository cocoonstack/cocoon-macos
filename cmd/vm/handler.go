package vm

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/cocoonstack/cocoon-macos/qemu"
	"github.com/cocoonstack/cocoon/types"
)

// Handler implements Actions by driving QEMU + OpenCore on an x86 Linux/KVM host.
type Handler struct{}

// NewHandler returns the default QEMU-backed handler.
func NewHandler() *Handler { return &Handler{} }

func (h *Handler) Create(cmd *cobra.Command, args []string) error {
	return todo("vm create", "P2: derive a per-VM qcow2 overlay + OpenCore config from image %q", args[0])
}

func (h *Handler) Run(cmd *cobra.Command, args []string) error {
	name, _ := cmd.Flags().GetString("name")
	cpus, _ := cmd.Flags().GetInt("cpus")
	mem, _ := cmd.Flags().GetString("memory")
	vnc, _ := cmd.Flags().GetInt("vnc")
	sshPort, _ := cmd.Flags().GetInt("ssh-port")
	spec := qemu.Spec{Name: name, Image: args[0], CPUs: cpus, Memory: mem, VNCDisp: vnc, SSHPort: sshPort}
	_ = spec.Args() // P2: launch qemu-system-x86_64 with these args
	return todo("vm run", "P2: create per-VM overlay from %q + launch QEMU (spec=%+v)", args[0], spec)
}

func (h *Handler) Start(cmd *cobra.Command, args []string) error {
	return todo("vm start", "P2: re-launch QEMU for %v", args)
}

func (h *Handler) Stop(cmd *cobra.Command, args []string) error {
	return todo("vm stop", "P2: ACPI/QMP shutdown for %v", args)
}

func (h *Handler) List(cmd *cobra.Command, args []string) error {
	// P2: load records from the JSON store; reuse cocoon's types.VM shape for output parity.
	vms := []*types.VM{}
	out, err := json.MarshalIndent(vms, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(out))
	return nil
}

func (h *Handler) Inspect(cmd *cobra.Command, args []string) error {
	return todo("vm inspect", "P2: print stored types.VM for %q", args[0])
}

func (h *Handler) Console(cmd *cobra.Command, args []string) error {
	return todo("vm console", "P2: attach to VNC/serial for %q", args[0])
}

func (h *Handler) RM(cmd *cobra.Command, args []string) error {
	return todo("vm rm", "P2: remove overlay + record for %v", args)
}

func todo(what, format string, a ...any) error {
	return fmt.Errorf("%s: not implemented yet — %s", what, fmt.Sprintf(format, a...))
}
