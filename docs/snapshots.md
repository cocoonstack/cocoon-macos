# Snapshot, Clone & Data Disks

## Snapshot / restore

```bash
cocoon-macos vm stop m1
cocoon-macos vm snapshot m1 --tag clean
cocoon-macos vm restore  m1 --tag clean   # --force to stop+restore+relaunch a running VM
```

Snapshots are **offline qcow2-internal** (`qemu-img snapshot`, VM stopped) and capture **disk state
only**. Live RAM snapshot is intentionally impossible here: `-cpu +invtsc` makes the macOS guest
non-migratable, so resume-from-RAM can't work. This is by design (see [Roadmap](roadmap.md)).

## Clone

```bash
cocoon-macos vm clone m1 -n m2 --ssh-port 2223 --random-smbios
```

A clone bakes a fresh copy-on-write overlay on the **shared base** (never on SRC's per-VM overlay,
which would break on `vm rm m1`). It cold-boots a **fresh Apple identity** when `--random-smbios`
is given to the clone (or inherited automatically when SRC already carries one) — without it, the
clone reuses SRC's serial and MAC. Clones copy SRC's data disks and can add more; a clone-of-a-clone
keeps a correct backing chain. SRC must be stopped while its OVMF variables and data disks are copied.

## Data disks (`--data-disk`)

`--data-disk` (on `run` / `create` / `clone`) attaches empty qcow2 data disks. It is repeatable with
comma-separated `key=value`:

```bash
cocoon-macos vm run <IMAGE> --data-disk size=20G --data-disk name=scratch,size=50G
```

- **Keys:** `size=` (required, `units.RAMInBytes` syntax e.g. `20G`, min 16 MiB) and `name=`
  (optional, `[a-z][a-z0-9_-]{0,19}`, default `data1`, `data2`, …; duplicates error).
- **At most 4 disks.** macOS has no virtio-blk driver (the OS disk rides AHCI), so data disks take the
  `ich9-ahci` controller's free SATA ports — `OpenCoreBoot` (sata.2) and `MacHDD` (sata.4) leave
  ports 0, 1, 3, 5.
- They snapshot/restore and clone with the VM.
- **Unlike cocoon, `fstype=` / `mount=` / `directio=` are rejected**: a macOS guest has no
  cloud-init/agent to partition, format, or mount the disk. It is attached **raw** — format it in the
  guest with Disk Utility or `diskutil`.
