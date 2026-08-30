# VM Boot & Firmware

How a macOS guest boots, and the current desktop/GUI status. Display, GPU, and
Setup-Assistant limitations are in [Known issues](known-issues.md).

## Boot recipe

Every VM boots the same way on Intel and AMD:

- **Firmware:** the shared LongQT OpenCore loader (`OpenCore.qcow2`) + 4 MB OVMF `CODE`/`VARS`,
  provisioned once by `doctor.sh` and reused by every VM. Firmware is external (an `-drive`), never
  baked into the macOS qcow2, so one image boots on both Intel and AMD hosts and the loader can be
  upgraded without rebuilding it.
- **CPU:** `Skylake-Client` spoofing `GenuineIntel` with `-hle,-rtm` (TSX off), `+invtsc`, and
  `vmware-cpuid-freq=on`. The TSC flags are load-bearing: without them macOS self-calibrates the TSC
  and spins pathologically under nested KVM on first boot. `--cpus` must be a positive even number;
  create/run/clone reject odd counts before VM state is created because this macOS stack does not boot them.
- **OpenCore picker:** the shipped `OpenCore.qcow2` template boots the default entry immediately with
  no picker UI (a visible picker can't be driven reliably headlessly — OpenCanopy cancels its
  `Timeout` countdown on stray USB-enumeration input and then waits forever). `--random-smbios`
  additionally patches `config.plist` in place to inject a per-VM SMBIOS identity; the default
  `vm create` / `vm run` path leaves the template's config untouched.
- **AMD:** `kvm.ignore_msrs=1` is set host-wide (macOS reads MSRs an AMD host lacks).

## GUI renders; Setup-Assistant skip is WIP

The full Tahoe desktop **does render over VNC** — testbed-verified: boot → login window (the `cocoon`
user) → type `cocoon` → Finder + Dock + menu bar + desktop widgets, all repainting normally.

What's left for a *fully unattended* boot-to-desktop is auto-skipping the first-run Setup Assistant
plus auto-login. The **post-SA recipe is validated** on a testbed VM: complete home +
`com.apple.SetupAssistant` `DidSee*`/`LastSeen*` markers + auto-login (`autoLoginUser` +
`/etc/kcpassword` written via `perl pack`) + keyboard-wizard suppress + `pmset` no-sleep.

**The blocker:** a fresh macOS 26 (Tahoe) clone boots to the *system* Setup Assistant
(`_mbsetupuser` / `SetupAssistantSpringboard`) and it resists every marker-based skip tried
(`.AppleSetupDone`, complete home from the User Template, `.skipbuddy`, `DidSee*`, auto-login, a
killsa daemon, removing `/var/db/ConfigurationProfiles` [SIP-blocked]). macOS 14+ broke the classic
`.AppleSetupDone` skip, and the keyboard does not register at the SA — so the only reliable automated
skip is a **mouse/OCR click-through of the SA wizard** (the install-stage OCR machinery), not yet
implemented. Until then, `:26` is SSH/VNC-login-usable but the GUI lands at the Setup Assistant.
