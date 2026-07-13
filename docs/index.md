# cocoon-macos

macOS VM engine for x86 Linux/KVM, built on
[cocoon](https://github.com/cocoonstack/cocoon). Boots real macOS guests
(Sequoia 15, Tahoe 26) as fully-virtualized [QEMU](https://www.qemu.org/)/KVM
VMs via [OpenCore](https://github.com/acidanthera/OpenCorePkg) + OVMF. A GitHub
Action installs macOS from scratch and publishes a golden qcow2 to ghcr; a thin
Go CLI clones that image and boots VMs from it.

```
cocoon-macos CLI ──► image: pull the golden macOS qcow2 from ghcr (parallel Range, sha256)
                 ──► vm: create/run/console/clone ── QEMU + OpenCore + OVMF, CoW disk, CNI real IP
                 ──► snapshot: save/restore ── offline qcow2-internal, per-VM Apple identity
```

## Guides

- [Installation](install.md) — host prerequisites, the doctor script (deps +
  firmware), the state directory, and building the CLI
- [CLI reference](cli.md) — the `image` and `vm` commands, key flags, and what
  `vm run` does under the hood
- [Images](images.md) — golden images on ghcr, the local cloudimg store, and the
  parallel-Range `image pull`
- [VM boot & firmware](vm.md) — the OpenCore/OVMF/CPU recipe, the desktop/GUI
  status, and the Setup-Assistant blocker
- [Networking & VNC](networking.md) — `--net user/tap/bridge/cni`, TC-redirect
  real LAN IP, and per-mode / per-start VNC exposure
- [Snapshots, clone & data disks](snapshots.md) — offline snapshots, CoW clones
  with a fresh identity, and extra data disks
- [CI image pipeline](image-pipeline.md) — how the GitHub Action installs macOS
  and publishes the golden images
- [E2E regression](e2e.md) — the `e2e.sh` lifecycle regression (`[DUMMY]` +
  `[REAL]` tiers)
- [Known issues](known-issues.md) — no-GPU video, display-sleep VNC blanking,
  and the Setup Assistant
- [Roadmap](roadmap.md) — what's planned, and what's intentionally out of scope

## Features

- **Fully-automated macOS install** — CI boots OpenCore, erases APFS, drives the
  installer by OCR, and publishes a golden qcow2 (Tahoe 26 / Sequoia 15) to ghcr
- **SSH-ready golden images** — `tahoe:26` ships an admin `cocoon`/`cocoon` user,
  a complete home, and Remote Login on first boot (plus a `-base` tier)
- **Parallel Range image pull** — the multi-GB qcow2 is pulled in 8 concurrent
  HTTP Range chunks with an sha256 digest check (oras-go for auth; no `oras` binary)
- **COW overlays** — an instant copy-on-write clone of the immutable golden base
  per VM
- **Per-VM Apple identity** — `--random-smbios` injects a unique serial/MLB/UUID/ROM
  (guest MAC = ROM) into a per-VM OpenCore, so clones never share a serial
- **CNI networking with TC redirect** — `--net cni` joins cocoon's forwarding
  plane so the guest DHCPs a real LAN IP; also `user`/`tap`/`bridge`
- **Reachable VNC** — loopback VNC on user/tap/bridge; a host-side proxy fronts
  CNI VNC on the host port (password required); launch-scoped, off by default
- **Snapshot, clone & restore** — offline qcow2-internal snapshots and CoW clones
  that cold-boot a fresh Apple identity
- **Data disks** — up to 4 extra qcow2 data disks on the AHCI ports the OS disk
  and OpenCore leave free (macOS has no virtio-blk driver)
- **Intel & AMD** — one boot recipe (Skylake-Client spoofing GenuineIntel + the
  LongQT OpenCore) boots identically on both; `ignore_msrs` auto-set on AMD
- **Docker-like CLI** — `create`, `run`, `start`, `stop`, `list`, `inspect`,
  `console`, `rm`, `snapshot`, `restore`, `clone`
- **Built on cocoon** — imports cocoon's `cloudimg` store, `network` plane, and
  copy-on-write conventions rather than reimplementing them

## Repository

Source and issue tracker:
[github.com/cocoonstack/cocoon-macos](https://github.com/cocoonstack/cocoon-macos).
Upstream engine:
[github.com/cocoonstack/cocoon](https://github.com/cocoonstack/cocoon)
(Lightweight MicroVM engine with Cloud Hypervisor and Firecracker backends).
