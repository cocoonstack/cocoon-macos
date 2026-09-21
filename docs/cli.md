# CLI Reference

The CLI mirrors cocoon's `vm` / `image` command surface, trimmed to the macOS VM path.
Run the examples in a root shell, or prefix commands with `sudo` for the default state root and host networking.

## Images

```bash
# pull the golden qcow2 from ghcr into the local store (cocoon cloudimg; /var/lib/cocoon-macos)
cocoon-macos image pull ghcr.io/cocoonstack/cocoon-macos/tahoe:26
cocoon-macos image list        # table (NAME TYPE SIZE DIGEST CREATED); -o json for JSON
cocoon-macos image inspect <ref>
cocoon-macos image rm <ref>
```

`image pull` also accepts a plain http(s) URL in place of a ref, handled by the `cloudimg` backend
directly. A ref already present in the store is a no-op unless `--force` re-pulls it.

See [Images](images.md) for the store layout and the parallel-Range download.

## VMs

```bash
# clone the golden image into a per-VM overlay and boot it (x86 Linux + /dev/kvm).
# IMAGE is a store ref or a direct qcow2 path; firmware defaults to the doctor's install.
cocoon-macos vm run ghcr.io/cocoonstack/cocoon-macos/tahoe:26 \
  --name m1 --cpus 4 --memory 8192 --storage 100Gi --ssh-port 2222 --vnc 1 --random-smbios

cocoon-macos vm list           # table (NAME STATE CPU MEM NET VNC SSH IMAGE CREATED); -o json for JSON
cocoon-macos vm inspect m1     # full record as JSON
cocoon-macos vm stop m1
cocoon-macos vm rm m1
# also: create (no boot), start, console
```

- `create` scaffolds the VM (overlay, identity, network, record) without booting; `run` = `create` +
  boot; `start` boots a created/stopped VM.
- If the initial boot fails, `run` stops QEMU, tears down networking and removes the new VM state.
  Cleanup failures are returned alongside the boot error; `gc` can reclaim unowned residue.
- `vm stop` / `vm rm` (and the stop inside `vm restore --force`) first ask the guest to shut down
  (`system_powerdown` over QEMU's HMP monitor, the ACPI power button) and give it 10 s to halt; if
  QEMU is still running after that they send SIGTERM and SIGKILL at once. `vm stop --force` and
  `vm rm --force` skip the power-down and the window (`vm restore --force` only means "stop first"
  and keeps them). If the monitor cannot be reached the old SIGTERM-then-10 s-then-SIGKILL path runs
  instead; a record whose QEMU is already gone skips the monitor entirely.
- `--storage` expands the new VM's qcow2 system disk before boot. It accepts values such as `100Gi`
  or a byte count, never shrinks an image, and is inherited by `clone` unless explicitly overridden.

Networking (`--net`) and VNC (`--vnc` / `--vnc-password`) are covered in
[Networking & VNC](networking.md); snapshot/clone and `--data-disk` in
[Snapshot, Clone & Data Disks](snapshots.md).

`--hugepages` (on `run` / `create` / `clone`) backs guest RAM with 2 MiB
hugepages for lower TLB/EPT overhead. The host must have enough pages
reserved (`vm.nr_hugepages`) for the VM's full `--memory`, or QEMU fails to
start — the allocation is not best-effort. On `clone` the flag is only
applied when passed explicitly; otherwise the source VM's setting carries
over.

`--exit-on-reboot` on `create` / `run` / `clone` is for VMs owned by an external
supervisor. It persists with the VM and is inherited by clones. QEMU's
`-no-reboot` exit skips the normal `vm stop` cleanup, so the supervisor must
recover the existing record with `vm start`; standalone VMs keep QEMU's normal
in-process reboot behavior.

## Housekeeping

```bash
cocoon-macos gc    # reclaim what no VM record owns
```

`gc` runs cocoon's netns and TAP collectors under cocoon-macos's own `cm` scope, so a `cm-<vmid>`
netns or `cm<vmid8>-<nic>` TAP whose VM record is gone (a `create` or `clone` killed between network
provisioning and the record write) is removed; a VM dir without a record is reset the way a
same-name re-create resets it (refused while a QEMU still references it); `pull-*.qcow2` temp files
older than an hour under the state root are deleted. It first takes, exclusively, the provisioning
lock that every `create` and `clone` holds shared from before its VM dir exists until its record is
written, so no VM can appear during the sweep, then the lock of every VM present, and refuses to
run while any of them is held. The netns and TAP collectors are host-global within the `cm` family,
so they run only for a state root that has held a VM: one with a VM dir under `vms/`, or with the
`vms/.locks/.provisioned` marker, which `create` and `clone` write the moment they reach network
provisioning (whatever `--net` mode, so a root of `--net user` VMs counts too) and which every path
that removes a VM dir (`vm rm`, the same-name re-create, `gc` itself) writes before removing it, so
the evidence outlives the last VM. Under a root without either (a
mistyped `--state-dir`, an unset `$COCOON_MACOS_HOME`, a root that only ever pulled images) `gc`
still removes the root's own stale pull temps but warns and leaves the host's netns and TAPs alone;
a state root that does not exist at all is refused. A co-hosted cocoon's `gc` never touches the `cm`
family and this verb never touches cocoon's. Use one cocoon-macos state root per host: the `cm`
network namespace is shared across roots. An unreadable or invalid VM record aborts the ownership
snapshot and prevents new orphan collection; a read error is not evidence that the VM is absent.

## What `vm run` does

1. `qemu-img create -b <golden> overlay.qcow2` — instant copy-on-write clone of the golden image.
2. Copy a per-VM `OVMF_VARS`.
3. With `--random-smbios`, copy OpenCore per-VM and inject a generated identity into its
   `config.plist` `PlatformInfo/Generic` via a `qemu-nbd` mount — model stays `iMac19,1` (proven to
   boot Tahoe), only serial/MLB/UUID/ROM are randomized. The identity is recorded and shown by
   `vm inspect`.
4. Launch `qemu-system-x86_64` daemonized with the boot recipe (a `Skylake-Client` CPU spoofing
   `GenuineIntel`, `isa-applesmc` OSK, OVMF, the LongQT OpenCore loader, and the macOS qcow2). The
   same recipe boots macOS identically on Intel and AMD; on AMD it also sets `kvm.ignore_msrs=1`
   (macOS reads MSRs an AMD host lacks). See [Boot, Firmware & GUI](vm.md).

New VM names contain 1–63 ASCII letters, digits, dots, underscores or hyphens, and start with a
letter or digit. Create and clone also check the resolved monitor socket path against Linux's
107-byte limit before scaffolding; shorten `--state-dir` or `--name` if needed.

State is recorded under `--state-dir` / `$COCOON_MACOS_HOME` (default `/var/lib/cocoon-macos`).
The CLI resolves a relative state root and supplied CNI directories against the current working
directory. Local image and firmware paths are persisted as absolute paths, so start and clone do
not depend on the working directory used for create. Use the same state root on later commands.
`$COCOON_MACOS_LOG_LEVEL` sets the log level (`debug` / `info` / `warn` / `error`, default `info`).
