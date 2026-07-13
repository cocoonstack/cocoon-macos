# Roadmap

## Planned

- **Port cocoon's passthrough surface.** cocoon exposes extra VM hardware that cocoon-macos doesn't
  yet wire to QEMU; port it the same way as networking (Linux-tagged, reusing cocoon types):
  - **Hardware passthrough (VFIO)** — cocoon's `extend/vfio` (`vfio.Spec`/`Attacher`, BDF/sysfs) and
    `cmd/vm attach`. Maps to QEMU `-device vfio-pci,host=<BDF>` for GPU/NIC passthrough — the real fix
    for hardware-accelerated video (see [Boot, Firmware & GUI](vm.md)).
- **GUI boot-to-desktop** — finish the Setup-Assistant mouse/OCR click-through so `:26` boots straight
  to `cocoon`'s desktop with auto-login (see [Boot, Firmware & GUI](vm.md)).

## Intentionally out of scope

- **Live snapshot.** `-cpu +invtsc` makes the macOS guest non-migratable, so resume-from-RAM can't
  work; snapshot stays offline/disk-only + cold clone.
- **A `qemu` Hypervisor backend *inside* cocoon** (so cocoon's own `cmd/core` dispatches macOS VMs)
  is a separate later phase — cocoon-macos currently **imports** cocoon's libraries (`cloudimg`,
  `network`, storage/CoW conventions) rather than the reverse.
- **Registering SMBIOS identities with iServices / App Store.** `--random-smbios` produces unique
  identities, but making them *valid* (not just unique) for Apple services is the consumer's policy
  concern and carries Apple-ID ban risk at fleet scale — so it is not done here.
