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
  - `ghcr.io/cocoonstack/cocoon-macos/tahoe:26` — **SSH-ready**: provisioned via Recovery Terminal
    (admin user `cocoon`/`cocoon`, a complete home, Remote Login/SSH on first boot). SSH + VNC login
    work (verify stage confirms `cocoon@…`, macOS 26.5). **Known limitation:** the GUI first boot
    still lands at the macOS 26 system Setup Assistant — see *Boot-to-desktop (WIP)* below.
- **CLI** (`cocoon-macos vm …`) clones a golden image (copy-on-write qcow2 overlay) and launches QEMU.
- **Per-VM identity** (`--random-smbios`, testbed-verified): injects a unique Apple SMBIOS
  (serial/MLB/UUID/ROM, with the guest NIC MAC set to the ROM) into a per-VM OpenCore so clones
  don't all boot as the shipped placeholder serial. Confirmed in-guest via `system_profiler` —
  two clones get two distinct serials, each matching what was injected.
- **Image management** (`cocoon-macos image pull|list|inspect|rm`): pulls the golden qcow2 from
  ghcr into a content-addressed local store — cocoon's `cloudimg` backend imported directly — so
  `vm run <ref>` bakes a CoW overlay on the immutable shared base. **qcow2-only** (no OCI image
  layers; `oras` is just the ghcr transport for the qcow2 blob).
- **Networking** (`--net user|tap|cni|bridge`): user-mode SLIRP + `--ssh-port` hostfwd by default;
  `tap`/`bridge`/`cni` auto-create a host TAP via cocoon's network plane (imported `network/bridge`
  + `network/cni`), so macOS VMs join the **same** bridge/CNI forwarding plane as cocoon's CH/FC
  VMs on the node. The guest MAC stays = SMBIOS ROM. Auto-create is Linux-only (CAP_NET_ADMIN);
  user-mode + a pre-created `--tap` work everywhere.
- **Snapshot / restore / clone** (`vm snapshot|restore|clone`): offline qcow2-internal snapshots
  (`qemu-img snapshot`, VM stopped — `-cpu +invtsc` blocks live RAM snapshot, so this is disk-state
  only) and CoW clones that cold-boot a **fresh** Apple identity, so two clones never share a serial/MAC.

## CLI

```bash
go build -o cocoon-macos .

# pull the golden qcow2 from ghcr into the local store (cocoon cloudimg; /var/lib/cocoon-macos)
cocoon-macos image pull ghcr.io/cocoonstack/cocoon-macos/tahoe:26
cocoon-macos image list        # table (NAME TYPE SIZE DIGEST CREATED); -o json for JSON. also: inspect, rm

# one-time: make the host ready — checks /dev/kvm + qemu + nbd, installs missing deps, and
# provisions the shared OpenCore + OVMF firmware into <state-dir>/firmware (reused by every VM).
# Works the same on Intel and AMD; you never pick "OpenCore vs LongQT".
sudo scripts/doctor.sh

# clone the golden image into a per-VM overlay and boot it (x86 Linux + /dev/kvm).
# IMAGE is a store ref (above) or a direct qcow2 path; firmware defaults to the doctor's install.
cocoon-macos vm run ghcr.io/cocoonstack/cocoon-macos/tahoe:26 \
  --name m1 --cpus 4 --memory 8192 --ssh-port 2222 --vnc 1 --random-smbios

cocoon-macos vm list           # table (NAME STATE CPU MEM NET VNC SSH IMAGE CREATED); -o json for JSON
cocoon-macos vm inspect m1     # full record as JSON
cocoon-macos vm stop m1
cocoon-macos vm rm m1
# also: create (no boot), start, console

# networking — join the host's bridge/CNI plane instead of user-mode SLIRP (Linux):
cocoon-macos vm run <IMAGE> --net bridge --bridge br0 --random-smbios  …   # auto-creates a TAP on br0
cocoon-macos vm run <IMAGE> --net cni    --random-smbios  …               # CNI: TAP in a netns
cocoon-macos vm run <IMAGE> --net tap    --tap tap0       …               # use a pre-created TAP verbatim

# snapshot / restore / clone (VM stopped for snapshot/restore; clone gets a unique identity):
cocoon-macos vm snapshot m1 --tag clean
cocoon-macos vm restore  m1 --tag clean          # --force to stop+restore+relaunch a running VM
cocoon-macos vm clone    m1 -n m2 --ssh-port 2223 --random-smbios
```

`vm run` does: `qemu-img create -b <golden> overlay.qcow2` (instant CoW clone) → copy a
per-VM `OVMF_VARS` → launch `qemu-system-x86_64` (recipe in `qemu/launch.go`: a `Skylake-Client-v4`
CPU spoofing GenuineIntel + `isa-applesmc` OSK + OVMF + the LongQT OpenCore loader + the macOS
qcow2) daemonized, recording state under `--state-dir` / `$COCOON_MACOS_HOME` (default `/var/lib/cocoon-macos`, mirroring cocoon's `/var/lib/cocoon`). The same recipe boots macOS identically on Intel and AMD hosts; on AMD it also sets `kvm.ignore_msrs=1` (macOS reads MSRs AMD lacks).

With `--random-smbios`, `create` also copies OpenCore per-VM and injects a generated identity
into its `config.plist` `PlatformInfo/Generic` (via `qemu-nbd` mount); the model stays `iMac19,1`
(proven to boot Tahoe) and only serial/MLB/UUID/ROM are randomized. The assigned identity is
recorded and shown by `vm inspect`.

QEMU's VNC is loopback-only (`127.0.0.1:590<vnc>`) and offers `None` auth, which **macOS
Screen Sharing hangs on**. Pass `--vnc-password <≤8 chars>` to start QEMU with `password=on`
(set via the monitor post-launch) so Screen Sharing prompts and connects; tunnel first with
`ssh -L 5901:127.0.0.1:5901 <host>`. Plain VNC clients (RealVNC/TigerVNC) work without a password.

**Display sleep blanks VNC.** macOS only repaints the emulated framebuffer while the display is
awake; once it sleeps (~idle), VNC shows a blank **white/black** screen with just the cursor even
though the guest is healthy (SSH works, WindowServer is up). It is *not* a GPU/driver problem — a
mouse move repaints it, and the full Tahoe desktop renders fine (Finder/Dock/menu bar). The golden
image's first-boot daemon now runs `pmset -a displaysleep 0 disablesleep 1` system-wide (covers the
pre-login loginwindow) so the framebuffer stays painted; older images need a `setup`-stage rebuild.

## CI image pipeline (`.github/workflows/build-macos-image.yml`, `scripts/build-qemu-macos.sh`)

`workflow_dispatch` with a `macos` version + a `stage`. **`macos`** selects which OS to build and
where it lands on ghcr (the build script derives `MACOS_SHORTNAME`/`GHCR_REPO`/`GHCR_TAG` from it):

| `macos` | fetch-macOS shortname | ghcr repo:tag |
|---------|-----------------------|---------------|
| `tahoe` (default) | `tahoe` | `…/tahoe:26` — macOS 26, the **last Intel** macOS |
| `sequoia` | `sequoia` | `…/sequoia:15` — macOS 15 (n-1, still security-supported) |

`stage` controls how far the build goes (shown for tahoe:26; the actual repo:tag follows `macos`):

| stage | what |
|-------|------|
| `boot` | smoke: boot OpenCore → macOS Recovery (proves KVM + OpenCore + recovery) |
| `install` | full install from scratch → capture → push `<repo>:<tag>-base` (~65 min) |
| `setup` | pull `<repo>:<tag>-base` → boot Recovery → `provision-macos.sh` (SA-skip recipe + user + SSH) → push `<repo>:<tag>` |
| `slim` | pull `<repo>:<tag>` → boot → reclaim stale clusters → re-push `<repo>:<tag>` (smaller) |
| `verify` | pull `<repo>:<tag>` → boot → confirm login + SSH (`cocoon@localhost`) |

Same OSX-KVM (multi-version OpenCore) + the same provision recipe build either OS — only the
recovery shortname + tag differ. This pipeline is **image-only** (no Go); the CLI end-to-end
(`vm run` + `--random-smbios`) is exercised on a KVM testbed, keeping image and Go CI separate.

Automation primitives (`scripts/qmp-input.py`): QMP absolute mouse click/move, keyboard
type/chord, **tesseract+PIL OCR-click and title routing** (drives the macOS GUI installer where
buttons can't be reached by keyboard), HMP `screendump`. Provisioning (`scripts/provision-macos.sh`)
runs in the Recovery Terminal against the installed Data volume (`dscl -f` offline user,
`.AppleSetupDone`, first-boot LaunchDaemon for Remote Login).

Key host facts: GitHub `ubuntu-latest` exposes `/dev/kvm` (needs `chmod 666`); macOS Tahoe 26
is the last Intel-supporting macOS, so this x86 path has a finite shelf life.

## Boot-to-desktop (GUI renders; auto-skip of the Setup Assistant is the remaining WIP)

The full Tahoe desktop **does render over VNC** — testbed-verified: boot → login window (the `cocoon`
user) → type `cocoon` → Finder + Dock + menu bar + desktop widgets, all repainting normally. The
earlier "white/black VNC" was purely **display sleep** (see the VNC note above), now fixed at the
image level. What's left for a *fully unattended* boot-to-desktop is auto-skipping the first-run
Setup Assistant + auto-login.

The `desktop` build stage + `provision-macos.sh` aim to make `:26` boot straight to `cocoon`'s
desktop (auto-login, no Setup Assistant). The **post-SA recipe is validated** (proven on a testbed
VM): complete home + `com.apple.SetupAssistant` `DidSee*`/`LastSeen*` (Buddy=build `25F71`,
Cloud=product `26.5`) + auto-login (`autoLoginUser` + `/etc/kcpassword` written via `perl pack` —
macOS `/bin/bash` 3.2 has no `printf \xHH`) + keyboard-wizard suppress + `pmset` no-sleep.

**The blocker:** a fresh macOS 26 Tahoe clone boots to the *system* Setup Assistant
(`_mbsetupuser` / `SetupAssistantSpringboard`) and it resists every marker-based skip we tried
(`.AppleSetupDone`, complete home from User Template, `.skipbuddy`, `DidSee*`, auto-login, a
correctly-named killsa daemon, removing `/var/db/ConfigurationProfiles` [SIP-blocked]). macOS 14+
broke the classic `.AppleSetupDone` skip. The keyboard does not register at the SA, so the only
reliable automated skip is a **mouse/OCR click-through of the SA wizard** (the install-stage OCR
machinery) — not yet implemented. Until then, `:26` is SSH/VNC-login-usable but the GUI lands at SA.

## Why QEMU (not Apple VZ)

VZ on Apple Silicon caps ~2 macOS VMs/host and can't use the App Store; QEMU + OpenCore on x86
has neither limit (at the cost of per-VM identity + Apple-ID ban risk at fleet scale). See the
deep-research notes that motivated this project.

## E2E regression (`scripts/e2e.sh`)

A testbed lifecycle regression (modeled on cocoon's), two tiers:

- **`[DUMMY]`** — file-level lifecycle on a tiny throwaway qcow2, **no macOS boot**: image store
  (`pull`-less `list`/`rm`), `vm create` overlay-on-shared-base, `--random-smbios` serial/MAC
  uniqueness + MAC==ROM, `snapshot`/`restore` (qcow2-internal tag rollback), `clone` (CoW on the
  shared base + distinct identity + clone-of-clone backing), `stop` PID-reuse-safe teardown,
  `--net bridge` auto-TAP create + `tap_owned` + teardown-on-`rm` + user-`--tap` preservation +
  negative paths — with post-conditions asserting no leaked TAPs/procs and backing-chain integrity.
  Runs on any x86 Linux/KVM host; the `--random-smbios` rows need `root` + `nbd` + a real
  `CM_OPENCORE`, the `--net` rows need `root` + a test bridge (they `SKIP`, not `FAIL`, otherwise).
- **`[REAL]`** (`--real`) — boots `tahoe:26` from the store, passes the OpenCore picker over the HMP
  monitor, asserts SSH-ready (`sw_vers` 26.x) + in-guest serial == injected. Testbed-only.

```bash
sudo CM_BIN=./cocoon-macos CM_OPENCORE=.../OpenCore.qcow2 ./scripts/e2e.sh           # [DUMMY] (30 rows)
sudo CM_HOME=~/cm-demo CM_OPENCORE=... CM_OVMF_CODE=... CM_OVMF_VARS=... ./scripts/e2e.sh --real-only
```

The `[DUMMY]` tier is the behavioral gate today's CI structurally can't give (CI is pure `go
test`/`vet`/build — nothing opens `/dev/kvm` or mutates netlink); it belongs on a privileged
self-hosted KVM runner. `[REAL]` stays a manual/nightly testbed run (~15GB + cold boot).

## Roadmap

- **Port cocoon's bypass/passthrough surface.** cocoon exposes extra VM hardware that cocoon-macos
  doesn't yet wire to QEMU; port it the same way as networking (Linux-tagged, reusing cocoon types):
  - **Multi-disk / data disks** — cocoon's `types.DataDiskSpec` + `StorageRole` (`data`/`cow`/…) and
    `--data-disk`. Maps to extra QEMU `-drive`/`-device virtio-blk` per disk (attach/detach lifecycle).
  - **Hardware passthrough (VFIO)** — cocoon's `extend/vfio` (`vfio.Spec`/`Attacher`, BDF/sysfs) and
    `cmd/vm attach`. Maps to QEMU `-device vfio-pci,host=<BDF>` for GPU/NIC/etc. passthrough.
- **GUI boot-to-desktop** — finish the SA click-through (below).
- **Live snapshot** is intentionally *not* on the roadmap: `-cpu +invtsc` makes the macOS guest
  non-migratable, so resume-from-RAM can't work; snapshot stays offline/disk-only + cold clone.

## Out of scope (v0.x)

A `qemu` Hypervisor backend *inside* cocoon (so cocoon's own `cmd/core` dispatches macOS VMs) is a
separate later phase — cocoon-macos currently **imports** cocoon's libraries (`cloudimg`, `network`,
storage/CoW conventions) rather than the reverse. Per-VM SMBIOS injection (`--random-smbios`) is
implemented + testbed-verified, but registering those identities with iServices/App Store is the
consumer's policy concern — it needs validated (not just unique) serials and carries Apple-ID ban
risk at fleet scale — so it is not done here.
