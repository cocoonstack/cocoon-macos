# CI Image Pipeline

The golden images are built entirely by CI:
`.github/workflows/build-macos-image.yml` + `scripts/build-qemu-macos.sh`.

It is a `workflow_dispatch` with a `macos` version and a `stage`. This pipeline is **image-only**
(no Go); the CLI end-to-end (`vm run` + `--random-smbios`) is exercised separately on a KVM testbed,
keeping image and Go CI apart.

## `macos` — which OS

`macos` selects the OS to build and where it lands on ghcr (the build script derives
`MACOS_SHORTNAME` / `GHCR_REPO` / `GHCR_TAG` from it):

| `macos` | fetch-macOS shortname | ghcr repo:tag |
|---------|-----------------------|---------------|
| `tahoe` (default) | `tahoe` | `…/tahoe:26` — macOS 26, the **last Intel** macOS |
| `sequoia` | `sequoia` | `…/sequoia:15` — macOS 15 (n-1, still security-supported) |

The same OSX-KVM multi-version OpenCore and the same provision recipe build either OS — only the
recovery shortname and tag differ.

## `stage` — how far the build goes

Shown for `tahoe:26`; the actual `repo:tag` follows `macos`.

| stage | what |
|-------|------|
| `boot` | Smoke: boot OpenCore → macOS Recovery (proves KVM + OpenCore + recovery). |
| `install` | Full install from scratch → capture → push `<repo>:<tag>-base` (~65 min). |
| `setup` | Pull `<repo>:<tag>-base` → boot Recovery → `provision-macos.sh` (SA-skip recipe + user + SSH) → push `<repo>:<tag>`. |
| `desktop` | Pull `<repo>:<tag>` → boot → attempt auto-login + Setup-Assistant skip + slim → re-push `<repo>:<tag>` (WIP, currently blocked on the Setup Assistant — see [Known issues](known-issues.md)). |
| `slim` | Pull `<repo>:<tag>` → boot → reclaim stale clusters → re-push `<repo>:<tag>` (smaller). |
| `verify` | Pull `<repo>:<tag>` → boot → confirm login + SSH (`cocoon@localhost`). |

## Automation primitives

- **`scripts/qmp-input.py`** — QMP absolute mouse click/move, keyboard type/chord, **Tesseract + PIL
  OCR-click and title routing** (drives the macOS GUI installer where buttons can't be reached by the
  keyboard), and HMP `screendump`.
- **`scripts/provision-macos.sh`** — runs in the Recovery Terminal against the installed Data volume:
  offline `dscl -f` user, `.AppleSetupDone`, and a first-boot LaunchDaemon that enables Remote Login.

## Host facts

GitHub `ubuntu-latest` exposes `/dev/kvm` (needs `chmod 666`). macOS Tahoe 26 is the last
Intel-supporting macOS, so this x86 path has a finite shelf life.
