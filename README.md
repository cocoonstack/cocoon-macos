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
- **Per-VM identity** (`--random-smbios`, testbed-verified): injects a unique Apple SMBIOS
  (serial/MLB/UUID/ROM, with the guest NIC MAC set to the ROM) into a per-VM OpenCore so clones
  don't all boot as the shipped placeholder serial. Confirmed in-guest via `system_profiler` —
  two clones get two distinct serials, each matching what was injected.

## CLI

```bash
go build -o cocoon-macos .

# clone the golden image into a per-VM overlay and boot it (x86 Linux + /dev/kvm)
cocoon-macos vm run ghcr-pulled-tahoe.qcow2 \
  --name m1 --cpus 4 --memory 8192 --ssh-port 2222 --vnc 1 --random-smbios \
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

With `--random-smbios`, `create` also copies OpenCore per-VM and injects a generated identity
into its `config.plist` `PlatformInfo/Generic` (via `qemu-nbd` mount); the model stays `iMac19,1`
(proven to boot Tahoe) and only serial/MLB/UUID/ROM are randomized. The assigned identity is
recorded and shown by `vm inspect`.

## CI image pipeline (`.github/workflows/build-macos-image.yml`, `scripts/build-qemu-macos.sh`)

`workflow_dispatch` with `stage`:

| stage | what |
|-------|------|
| `boot` | smoke: boot OpenCore → macOS Recovery (proves KVM + OpenCore + Tahoe recovery) |
| `install` | full install from scratch → capture → push `tahoe:26-base` (~65 min) |
| `setup` | pull `tahoe:26-base` → boot Recovery → `provision-macos.sh` (skip SA + user + SSH) → push `tahoe:26` |
| `verify` | pull `tahoe:26` → boot → confirm login + SSH (`cocoon@localhost`) |

This pipeline is **image-only** (no Go); the CLI end-to-end (`vm run` + `--random-smbios`) is
exercised on a KVM testbed, keeping image and Go CI separate.

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
Per-VM SMBIOS injection (`--random-smbios`) is implemented + testbed-verified, but registering
those identities with iServices/App Store is the consumer's policy concern — it needs validated
(not just unique) serials and carries Apple-ID ban risk at fleet scale — so it is not done here.
