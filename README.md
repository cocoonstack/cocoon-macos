# cocoon-macos

Run **full macOS (Tahoe 26)** as a fully-virtualized **QEMU/KVM** guest on **x86 Linux** —
built entirely by CI. A GitHub Action installs macOS from scratch and publishes a golden
disk image to ghcr; a thin Go CLI clones that image and boots VMs from it.

## What works (proven in CI on GitHub Actions `ubuntu-latest`)

- **Fully-automated macOS Tahoe 26 install** (`stage=install`, ~50 min): OpenCore boot →
  `diskutil` erase (APFS) → OCR/keyboard-driven installer click-through → ~15 GB download +
  install → `RequestBootVarRouting` makes the install reboots auto-continue → Setup Assistant.
- **Golden images on ghcr:**
  - `ghcr.io/cocoonstack/cocoon-macos/tahoe:26-base` — installed macOS Tahoe 26 at first-run Setup Assistant.
  - `ghcr.io/cocoonstack/cocoon-macos/tahoe:26` — turnkey: provisioned via Recovery Terminal
    (Setup Assistant skipped, admin user `cocoon`/`cocoon`, Remote Login/SSH enabled on first boot).
- **CLI** (`cocoon-macos vm …`) clones a golden image (copy-on-write qcow2 overlay) and launches QEMU.

## CLI

```bash
go build -o cocoon-macos .

# clone the golden image into a per-VM overlay and boot it (x86 Linux + /dev/kvm)
cocoon-macos vm run ghcr-pulled-tahoe.qcow2 \
  --name m1 --cpus 4 --memory 8192 --ssh-port 2222 --vnc 1 \
  --opencore OpenCore.qcow2 --ovmf-code OVMF_CODE_4M.fd --ovmf-vars OVMF_VARS.fd

cocoon-macos vm list           # JSON of all VMs
cocoon-macos vm inspect m1
cocoon-macos vm stop m1
cocoon-macos vm rm m1
# also: create (no boot), start, console
```

`vm run` does: `qemu-img create -b <golden> overlay.qcow2` (instant CoW clone) → copy a
per-VM `OVMF_VARS` → launch `qemu-system-x86_64` (validated OSX-KVM recipe in `qemu/launch.go`:
Skylake-Client CPU spoofing GenuineIntel + `isa-applesmc` OSK + OVMF + OpenCore + the macOS
qcow2) daemonized, recording state under `$COCOON_MACOS_HOME` (default `~/.cocoon-macos`).

## CI image pipeline (`.github/workflows/build-macos-image.yml`, `scripts/build-qemu-macos.sh`)

`workflow_dispatch` with `stage`:

| stage | what |
|-------|------|
| `boot` | smoke: boot OpenCore → macOS Recovery (proves KVM + OpenCore + Tahoe recovery) |
| `install` | full install from scratch → capture → push `tahoe:26-base` (~65 min) |
| `setup` | pull `tahoe:26-base` → boot Recovery → `provision-macos.sh` (skip SA + user + SSH) → push `tahoe:26` |
| `verify` | pull `tahoe:26` → boot → confirm login + SSH (`cocoon@localhost`) |
| `cli` | build the Go CLI → `cocoon-macos vm run tahoe:26` → SSH (end-to-end CLI proof) |

Automation primitives (`scripts/qmp-input.py`): QMP absolute mouse click/move, keyboard
type/chord, **tesseract+PIL OCR-click and title routing** (drives the macOS GUI installer where
buttons can't be reached by keyboard), HMP `screendump`. Provisioning (`scripts/provision-macos.sh`)
runs in the Recovery Terminal against the installed Data volume (`dscl -f` offline user,
`.AppleSetupDone`, first-boot LaunchDaemon for Remote Login).

Key host facts: GitHub `ubuntu-latest` exposes `/dev/kvm` (needs `chmod 666`); macOS Tahoe 26
is the last Intel-supporting macOS, so this x86 path has a finite shelf life.

## Why QEMU (not Apple VZ)

VZ on Apple Silicon caps ~2 macOS VMs/host and can't use the App Store; QEMU + OpenCore on x86
has neither limit (at the cost of per-VM identity + Apple-ID ban risk at fleet scale). See the
deep-research notes that motivated this project.

## Out of scope (v0.x)

cocoon engine integration (a `qemu` Hypervisor backend in cocoon) is a separate later phase.
iServices/App Store (per-VM SMBIOS injection) is a designed-in hook, not enabled here.
