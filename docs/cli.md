# CLI Reference

The CLI mirrors cocoon's `vm` / `image` command surface, trimmed to the macOS VM path.

## Images

```bash
# pull the golden qcow2 from ghcr into the local store (cocoon cloudimg; /var/lib/cocoon-macos)
cocoon-macos image pull ghcr.io/cocoonstack/cocoon-macos/tahoe:26
cocoon-macos image list        # table (NAME TYPE SIZE DIGEST CREATED); -o json for JSON
cocoon-macos image inspect <ref>
cocoon-macos image rm <ref>
```

See [Images](images.md) for the store layout and the parallel-Range download.

## VMs

```bash
# clone the golden image into a per-VM overlay and boot it (x86 Linux + /dev/kvm).
# IMAGE is a store ref or a direct qcow2 path; firmware defaults to the doctor's install.
cocoon-macos vm run ghcr.io/cocoonstack/cocoon-macos/tahoe:26 \
  --name m1 --cpus 4 --memory 8192 --ssh-port 2222 --vnc 1 --random-smbios

cocoon-macos vm list           # table (NAME STATE CPU MEM NET VNC SSH IMAGE CREATED); -o json for JSON
cocoon-macos vm inspect m1     # full record as JSON
cocoon-macos vm stop m1
cocoon-macos vm export m1 registry.example.com/team/macos-custom:v1
cocoon-macos vm rm m1
# also: create (no boot), start, console
```

- `create` scaffolds the VM (overlay, identity, network, record) without booting; `run` = `create` +
  boot; `start` boots a created/stopped VM.
- `run` is atomic: if the boot fails it removes everything it just created (no half-made VM left
  behind).

Networking (`--net`) and VNC (`--vnc` / `--vnc-password`) are covered in
[Networking & VNC](networking.md); snapshot/clone and `--data-disk` in
[Snapshot, Clone & Data Disks](snapshots.md).

`--hugepages` (on `run` / `create` / `clone`) backs guest RAM with 2 MiB
hugepages for lower TLB/EPT overhead. The host must have enough pages
reserved (`vm.nr_hugepages`) for the VM's full `--memory`, or QEMU fails to
start — the allocation is not best-effort. On `clone` the flag is only
applied when passed explicitly; otherwise the source VM's setting carries
over.

## What `vm run` does

1. `qemu-img create -b <golden> overlay.qcow2` — instant copy-on-write clone of the golden image.
2. Copy a per-VM `OVMF_VARS`.
3. With `--random-smbios`, copy OpenCore per-VM and inject a generated identity into its
   `config.plist` `PlatformInfo/Generic` via a `qemu-nbd` mount — model stays `iMac19,1` (proven to
   boot Tahoe), only serial/MLB/UUID/ROM are randomized. The identity is recorded and shown by
   `vm inspect`.
4. Launch `qemu-system-x86_64` daemonized with the boot recipe (a `Skylake-Client-v4` CPU spoofing
   `GenuineIntel`, `isa-applesmc` OSK, OVMF, the LongQT OpenCore loader, and the macOS qcow2). The
   same recipe boots macOS identically on Intel and AMD; on AMD it also sets `kvm.ignore_msrs=1`
   (macOS reads MSRs an AMD host lacks). See [Boot, Firmware & GUI](vm.md).

State is recorded under `--state-dir` / `$COCOON_MACOS_HOME` (default `/var/lib/cocoon-macos`).
`$COCOON_MACOS_LOG_LEVEL` sets the log level (`debug` / `info` / `warn` / `error`, default `info`).

## Export a custom image

```bash
cocoon-macos vm export m1 registry.example.com/team/macos-custom:v1
```

The command stops a running VM, flattens and compresses its qcow2 backing chain,
restarts the VM, then uploads the standalone disk as
`application/vnd.cocoonstack.os-image.v1+json`. Registry credentials come from the
standard Docker config. Upload happens after restart, so registry latency does not
extend VM downtime.

Use `--local-name NAME` to register the flattened qcow2 in the local cloud-image
store after a successful push. Later runs on the source node can then reuse it
without downloading it from the registry. A local-retention failure does not roll
back the already published OCI artifact; the error reports its manifest digest.

The current VNC display is preserved when the VM is restarted. VNC credentials are
intentionally not persisted, so a CNI VM whose password is no longer available must
pass `--vnc N --vnc-password PASSWORD`. Pass `--vnc -1` to disable VNC after the
export. A VM that was already stopped remains stopped.
