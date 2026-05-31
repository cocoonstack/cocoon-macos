# cocoon-macos

Run **full macOS (Tahoe 26)** as a fully-virtualized **QEMU/KVM** guest on **x86 Linux**,
with an automated CI pipeline that builds golden disk images and a thin Go CLI that
mirrors cocoon's command set.

## Goal

> Stand up macOS-on-QEMU as a self-contained cocoonstack project:
>
> 1. **Image automation** — a GitHub Action (`.github/workflows/build-macos-image.yml`
>    + `scripts/build-qemu-macos.sh`) that builds a golden `macos-tahoe-26.qcow2` on
>    `ubuntu-latest` and pushes it to `ghcr.io/cocoonstack/cocoon-macos/tahoe:26`,
>    modeled on cocoon's `os-image` / `cocoonstack/windows` QEMU build.
> 2. **CLI** — a thin Go wrapper mirroring cocoon's commands so `cocoon-macos vm create`
>    / `vm run` creates and boots a macOS VM via QEMU + OpenCore.

**Out of scope (for now):** cocoon engine integration (a `qemu` Hypervisor backend in
cocoon) is a later, separate phase. iServices/App Store — which need unique per-VM SMBIOS
injection (`MLB/ROM/SystemSerialNumber/SystemUUID/SystemProductName/board-id`), a built-in
`en0` NIC, and VMHide.kext — is a designed-in hook but **not enabled in v0.1**.

## Why QEMU (not Apple Virtualization.framework / VZ)

Verified late-May-2026: VZ on Apple Silicon is capped at **~2 macOS VMs per host** and its
guests **cannot use the App Store** (iCloud sign-in works since macOS 15, but App Store /
Apple Media Services / Find My / iCloud Backup / Wallet do not). QEMU + OpenCore on x86 has
no such cap and can reach full Apple services — at the cost of per-VM unique identity and
Apple-ID ban risk at fleet scale. Tahoe 26 is the **last Intel-supporting macOS** (27+ is
Apple-Silicon-only), so this x86 path has a finite shelf life.

## Status

Scaffold only. CLI compiles; all `vm` actions are stubs returning `not implemented`.

| Phase | What | State |
|-------|------|-------|
| P0 | KVM smoke + **macOS install-automation spike** (no autounattend equivalent — the load-bearing unknown) | TODO |
| P1 | Golden `tahoe-base.qcow2` (recovery + LongQT-sea OpenCore), SSH-enabled | TODO |
| P2 | Go CLI v0.1: `vm create/run/start/stop/list/inspect/console/rm` (qcow2 overlay + headless QEMU) | scaffold |
| P3 | CI image build → push ghcr | scaffold |
| P4 | (separate) cocoon `qemu` backend | not started |

## Layout

```
cmd/            CLI (cobra) — root + vm subcommand tree (copied from cocoon's pattern)
qemu/           qemu-system-x86_64 arg builder / launch (Spec.Args)
image/          OpenCore config templating + SMBIOS injection (later)
os-image/tahoe/ OpenCore EFI (LongQT-sea), fetch-macOS, install scripts
scripts/        build-qemu-macos.sh — the CI image-build script
.github/        build-macos-image.yml — the image-build Action
```

## Dependency on cocoon

Imports only `github.com/cocoonstack/cocoon/types` (the cross-platform-safe package).
`cmd/core`, `hypervisor`, and `cmd/vm` drag Linux-only `netlink`/CH deps and won't build on
darwin, so the cobra command tree is copied rather than imported. Local dev resolves cocoon
via `replace => ../cocoonv2`; CI pins a real pseudo-version.

## Dev

```bash
go build ./...        # builds the CLI (works on darwin; QEMU launch only runs on x86 Linux/KVM)
./cocoon-macos vm --help
```

Image builds and `vm run` require an **x86 Linux host with `/dev/kvm`** — not a Mac.
