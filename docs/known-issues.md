# Known Issues

## No GPU → hardware-accelerated video renders black

The display is `-device vmware-svga` (macOS's only supported virtual adapter): a **2D software
framebuffer with no Metal / GPU / VideoToolbox / IOSurface**. CPU-drawn content renders fine —
desktop, widgets, window chrome, and *static* images (e.g. a YouTube thumbnail) — but **live video
playback** uses a GPU-composited hardware overlay (AVPlayerLayer + IOSurface + VideoToolbox) with no
backing on a GPU-less VM, so the moving picture stays **black even though audio plays and the scrubber
advances**. Safari has no software-rendering fallback.

- **Workaround:** Chrome with hardware acceleration off (Settings → System → uncheck "Use hardware
  acceleration when available", relaunch) composites video in software into a layer the framebuffer
  can draw.
- **No display-device fix:** `qxl` / `virtio-gpu` / `std` have no macOS driver, so swapping `-vga` /
  `-device` cannot add acceleration.
- **Real fix:** GPU passthrough (VFIO `-device vfio-pci`) — see [Roadmap](roadmap.md).

## VNC blanks on display sleep

macOS only repaints the emulated framebuffer while the display is **awake**; once it sleeps (~idle),
VNC shows a blank white/black screen with just the cursor even though the guest is healthy (SSH works,
WindowServer is up). A mouse move repaints it — it is *not* a GPU/driver problem. The golden image's
first-boot daemon runs `pmset -a displaysleep 0 disablesleep 1` system-wide (covering the pre-login
loginwindow) to keep the framebuffer painted; older images need a `setup`-stage rebuild.

## GUI lands at the Setup Assistant

A fresh macOS 26 clone boots to the system Setup Assistant, which resists every offline marker-based
skip tried (macOS 14+ broke the classic `.AppleSetupDone` skip). `:26` is SSH/VNC-login-usable, but a
fully unattended boot-to-desktop needs a mouse/OCR click-through of the SA wizard — see the WIP detail
in [VM Boot & Firmware](vm.md) and the [Roadmap](roadmap.md).
