# Installation

## Prerequisites

- **x86-64 Linux** with **KVM** (`/dev/kvm` present and accessible). Both Intel and AMD hosts work —
  the same boot recipe applies to both; you never choose "OpenCore vs LongQT".
- `qemu-system-x86_64`, `qemu-nbd` (from `qemu-utils`), and NBD kernel module support.
- Enough disk for the golden image (~15 GB) plus per-VM CoW overlays.

## Build the CLI

```bash
go build -o cocoon-macos .
```

## Provision the host (`scripts/doctor.sh`)

Run once per host. `doctor.sh`:

1. Checks `/dev/kvm` and installs any missing dependencies (`qemu`, OVMF, `gdisk`, `dosfstools`),
2. Loads the `nbd` kernel module,
3. Provisions the **shared firmware** into `<state-dir>/firmware` — it downloads the LongQT OpenCore
   release and bakes a GPT/ESP `OpenCore.qcow2` plus the 4 MB OVMF `CODE`/`VARS`. Every VM reuses this
   one firmware install.

```bash
sudo scripts/doctor.sh
```

`vm run` fails with a "run scripts/doctor.sh" message when the firmware is absent. You can override
any piece with `--opencore` / `--ovmf-code` / `--ovmf-vars` on the VM commands.

## State directory

All persistent state lives under `--state-dir` / `$COCOON_MACOS_HOME`, default
`/var/lib/cocoon-macos` (mirroring cocoon's `/var/lib/cocoon`):

```
/var/lib/cocoon-macos/
├── firmware/     # shared OpenCore.qcow2 + OVMF_CODE/VARS (from doctor.sh)
├── cloudimg/     # content-addressed golden-image store (image pull)
└── vms/<name>/   # per-VM: disk.qcow2 overlay, OVMF_VARS, OpenCore overlay, vm.json, sockets
```
