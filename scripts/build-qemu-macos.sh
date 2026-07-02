#!/usr/bin/env bash
# Build a golden macOS qcow2 on an x86 Linux/KVM host and push it to ghcr. Which macOS is set by
# MACOS_SHORTNAME (+ GHCR_REPO/GHCR_TAG/QCOW2_NAME) — the workflow derives them from its `macos`
# input: tahoe=26 (the last Intel macOS) or sequoia=15. Boots the exact firmware stack production
# uses: the LongQT OpenCore baked by scripts/lib-firmware.sh + apt ovmf (OVMF_*_4M.fd) + a
# GenuineIntel Skylake-Client CPU (TSX off + invariant-TSC via CPUID; see qemu/launch.go for why every
# flag is load-bearing). kholia/OSX-KVM is cloned only for fetch-macOS-v2.py (the
# Apple recovery download).
#
# STAGE controls how far we go (the macOS install has no autounattend, so it is
# the iterated R&D spike — driven via the Action with retries):
# This is an IMAGE-only pipeline (no Go); the CLI is exercised separately on a testbed.
# (Stage names below use tahoe:26 as the example; the actual repo:tag is $GHCR_REPO:$GHCR_TAG.)
#   boot    — boot headless to the macOS Recovery/installer, screenshot proof
#   install — Recovery -> erase -> startosinstall headlessly, push <repo>:<tag>-base
#   setup   — provision the installed image (create cocoon user + SSH), push <repo>:<tag>
#   desktop — boot the image -> auto-login cocoon + skip Setup Assistant + slim, re-push <repo>:<tag>
#   verify  — boot the image + confirm SSH
set -euo pipefail

STAGE=${STAGE:-boot}
ROOT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
WORKDIR=${WORKDIR:-"$ROOT_DIR/work/qemu-build"}
ARTIFACT_DIR=${ARTIFACT_DIR:-"$WORKDIR/artifacts"}
OSX_KVM_DIR="$WORKDIR/OSX-KVM"

OSX_KVM_REPO=${OSX_KVM_REPO:-"https://github.com/kholia/OSX-KVM.git"}
MACOS_SHORTNAME=${MACOS_SHORTNAME:-"tahoe"}   # fetch-macOS-v2.py --shortname (non-interactive; tahoe = macOS 26)
OSK="ourhardworkbythesewordsguardedpleasedontsteal(c)AppleComputerInc"

OPENCORE_QCOW2="$WORKDIR/OpenCore.qcow2"          # LongQT OpenCore, baked by lib-firmware.sh
OVMF_CODE=${OVMF_CODE:-/usr/share/OVMF/OVMF_CODE_4M.fd}
OVMF_VARS_SRC=${OVMF_VARS_SRC:-/usr/share/OVMF/OVMF_VARS_4M.fd}
OVMF_VARS="$WORKDIR/OVMF_VARS.fd"                 # per-build writable copy of the apt VARS template

QCOW2_NAME=${QCOW2_NAME:-"macos-tahoe-26.qcow2"}
DISK_SIZE=${DISK_SIZE:-80G}
CPUS=${CPUS:-4}
MEMORY=${MEMORY:-8192}            # MiB
SSH_PORT=${SSH_PORT:-2222}
VNC_DISP=${VNC_DISP:-1}           # host 127.0.0.1:590<VNC_DISP>
MON_SOCK="$WORKDIR/monitor.sock"
QMP_SOCK="$WORKDIR/qmp.sock"
QMP_PY="$ROOT_DIR/scripts/qmp-input.py"
BOOT_SCREENSHOT_MINS=${BOOT_SCREENSHOT_MINS:-6}

# Framebuffer geometry — single source: configure_opencore forces this GOP mode and qmp-input.py's
# click mapping reads it via QMP_W/QMP_H. The two MUST match or every OCR-driven click scales
# off-target by the ratio. 1920x1080 is a standard OVMF GOP mode.
GOP_W=${GOP_W:-1920}
GOP_H=${GOP_H:-1080}
export QMP_W="$GOP_W" QMP_H="$GOP_H"

GHCR_REPO=${GHCR_REPO:-"ghcr.io/cocoonstack/cocoon-macos/tahoe"}
GHCR_TAG=${GHCR_TAG:-"26"}

QEMU_PID=""
mkdir -p "$WORKDIR" "$ARTIFACT_DIR"

log() { printf '[build-macos] %s\n' "$*"; }

# shellcheck source=scripts/lib-firmware.sh
source "$ROOT_DIR/scripts/lib-firmware.sh"

cleanup() {
  set +e
  [[ -n "$QEMU_PID" ]] && kill "$QEMU_PID" 2>/dev/null
  [[ -f "$WORKDIR/qemu.pid" ]] && kill "$(cat "$WORKDIR/qemu.pid")" 2>/dev/null
}
trap cleanup EXIT

require_kvm() {
  [[ -e /dev/kvm ]] || { log "FATAL: /dev/kvm missing — host has no KVM"; exit 1; }
  # GHA runners ship /dev/kvm as root:kvm 0660 and the runner user is not in the
  # kvm group, so QEMU gets EACCES; open it up (same as Docker-OSX's CI).
  sudo chmod 666 /dev/kvm 2>/dev/null || true
  log "KVM present: $(ls -l /dev/kvm)"
  echo 1 | sudo tee /sys/module/kvm/parameters/ignore_msrs >/dev/null 2>&1 || true
}

mon() { echo "$*" | socat - "UNIX-CONNECT:$MON_SOCK" >/dev/null 2>&1 || true; }
click() { python3 "$QMP_PY" "$QMP_SOCK" click "$1" "$2" 2>/dev/null || true; }
dclick() { python3 "$QMP_PY" "$QMP_SOCK" dclick "$1" "$2" 2>/dev/null || true; }
typestr() { python3 "$QMP_PY" "$QMP_SOCK" type "$1" 2>/dev/null || true; }
keys() { python3 "$QMP_PY" "$QMP_SOCK" key "$@" 2>/dev/null || true; }
chord() { python3 "$QMP_PY" "$QMP_SOCK" chord "$@" 2>/dev/null || true; }
ocrclick() {  # WORD [ymin ymax]; wakes the display (neutral move) before OCR so it doesn't read a slept-black screen
  python3 "$QMP_PY" "$QMP_SOCK" move 60 400 2>/dev/null || true
  sleep 1
  python3 "$QMP_PY" "$QMP_SOCK" ocrclick "$@" 2>&1 | sed 's/^/[ocr] /' || true
}

ocrtext() {  # wake display, then dump all on-screen OCR text (for adaptive installer-pane detection)
  python3 "$QMP_PY" "$QMP_SOCK" move 60 400 2>/dev/null || true
  python3 "$QMP_PY" "$QMP_SOCK" ocrtext 2>/dev/null || true
}

agreebtn() {  # click the active SLA "Agree" button (Disagree-paired, topmost) — not body text, not the greyed bg button
  python3 "$QMP_PY" "$QMP_SOCK" move 60 400 2>/dev/null || true
  python3 "$QMP_PY" "$QMP_SOCK" agreebtn 2>&1 | sed 's/^/[agree] /' || true
}

modalagree() {  # the SLA confirm-sheet "Agree" is grey-on-grey and OCR-unreadable, so click by position.
  # Measured at the forced 1920x1080 GOP: Tahoe's Agree center is (1018,526). The install window is
  # horizontally screen-centered (x=GOP_W/2+58) but sits high, so y can't derive from GOP_H — bracket
  # it downward to also catch Sequoia, whose longer body text wraps the button lower. Clicks stay in
  # the Agree column (never Disagree at ~899) and the modal sheet absorbs any miss.
  local x=$((GOP_W / 2 + 58)) y
  for y in 520 546 572 598; do click "$x" "$y"; sleep 1; done
}

screendump() {  # screendump <label> — via QMP, not HMP: the monitor `screendump` emits empty (6-byte)
  # files on this QEMU, which silently blinds boot_macintosh's picker_size check. QMP screendump
  # (the same path the OCR installer uses) writes a real PPM, so the artifact + picker detection work.
  local ppm="$WORKDIR/$1.ppm" png="$ARTIFACT_DIR/$1.png"
  python3 "$QMP_PY" "$QMP_SOCK" screendump "$ppm" 2>/dev/null || true
  sleep 1
  if [[ -s "$ppm" ]]; then
    pnmtopng "$ppm" >"$png" 2>/dev/null || cp "$ppm" "$png"
    rm -f "$ppm"; log "screenshot -> $1.png"
  else
    log "screenshot $1: empty frame"
  fi
}

setup_osx_kvm() {  # clone kholia/OSX-KVM — used ONLY for fetch-macOS-v2.py (Apple recovery download)
  [[ -d "$OSX_KVM_DIR" ]] || git clone --depth 1 "$OSX_KVM_REPO" "$OSX_KVM_DIR"
  python3 -m pip install --quiet --user requests click 2>/dev/null || true
}

prepare_firmware() {  # bake the production LongQT OpenCore + stage a writable OVMF_VARS from apt ovmf
  [[ -f "$OPENCORE_QCOW2" ]] || bake_opencore_qcow2 "$OPENCORE_QCOW2"
  [[ -f "$OVMF_VARS" ]] || cp "$OVMF_VARS_SRC" "$OVMF_VARS"
}

fetch_recovery() {
  cd "$OSX_KVM_DIR"
  if [[ ! -f BaseSystem.img ]]; then
    log "fetching macOS recovery (--shortname $MACOS_SHORTNAME) from Apple CDN"
    python3 fetch-macOS-v2.py --shortname "$MACOS_SHORTNAME"
    log "converting BaseSystem.dmg -> BaseSystem.img"
    dmg2img -i BaseSystem.dmg BaseSystem.img
  fi
  [[ -f "$QCOW2_NAME" ]] || qemu-img create -f qcow2 "$QCOW2_NAME" "$DISK_SIZE"
}

configure_opencore() {  # patch OpenCore config.plist; arg "hide" => HideAuxiliary+short Timeout so it auto-boots the installed macOS
  local hide="${1:-}"
  local oc="$OPENCORE_QCOW2"
  [[ -f "$oc" ]] || { log "OpenCore.qcow2 missing; skip OC patch"; return 0; }
  log "patching OpenCore config.plist: RequestBootVarRouting + ${GOP_W}x${GOP_H} + Timeout (qemu-nbd)"
  sudo modprobe nbd max_part=8 2>/dev/null || { log "no nbd module; skip OC patch"; return 0; }
  sudo qemu-nbd --connect=/dev/nbd0 -f qcow2 "$oc" 2>/dev/null || { log "qemu-nbd connect failed; skip"; return 0; }
  sleep 3
  sudo partprobe /dev/nbd0 2>/dev/null || true
  sudo mkdir -p /mnt/oc
  local part mounted=""
  for part in /dev/nbd0p1 /dev/nbd0p2 /dev/nbd0; do
    if sudo mount "$part" /mnt/oc 2>/dev/null; then mounted=1; break; fi
  done
  if [[ -n "$mounted" ]]; then
    local cfg
    cfg=$(sudo find /mnt/oc -iname config.plist 2>/dev/null | head -1)
    if [[ -n "$cfg" ]]; then
      # Force the GOP mode that qmp-input.py's click mapping (QMP_W/QMP_H) assumes. Stock apt
      # OVMF_VARS_4M.fd otherwise boots at OVMF's default resolution, and every OCR-driven click
      # would then scale off-target against the real framebuffer.
      sudo HIDE="$hide" RES="${GOP_W}x${GOP_H}" python3 - "$cfg" <<'PY' || true
import plistlib, os, sys
p = sys.argv[1]
d = plistlib.load(open(p, "rb"))
d.setdefault("Booter", {}).setdefault("Quirks", {})["RequestBootVarRouting"] = True
d.setdefault("UEFI", {}).setdefault("Output", {})["Resolution"] = os.environ["RES"]
b = d.setdefault("Misc", {}).setdefault("Boot", {})
if os.environ.get("HIDE") == "hide":
    b["HideAuxiliary"] = True   # hide EFI/recovery -> only the installed macOS remains
    b["Timeout"] = 5            # auto-boot it (no keypress needed)
else:
    b["Timeout"] = 8
plistlib.dump(d, open(p, "wb"))
print("[oc] patched config.plist (HIDE=%s) in %s" % (os.environ.get("HIDE"), p))
PY
    else
      log "config.plist not found on OpenCore EFI"
    fi
    sudo umount /mnt/oc 2>/dev/null || true
  else
    log "could not mount OpenCore EFI partition"
  fi
  sudo qemu-nbd --disconnect /dev/nbd0 2>/dev/null || true
}

launch_qemu() {
  cd "$OSX_KVM_DIR"
  log "launching QEMU headless (VNC 127.0.0.1:590$VNC_DISP, ssh :$SSH_PORT, monitor $MON_SOCK)"
  local args=(
    -enable-kvm -m "$MEMORY"
    -cpu Skylake-Client,-hle,-rtm,kvm=on,vendor=GenuineIntel,+invtsc,vmware-cpuid-freq=on,+ssse3,+sse4.2,+popcnt,+avx,+aes,+xsave,+xsaveopt,+pcid,+invpcid,+tsc-deadline,+rdtscp,+xsavec,check
    -machine q35
    -smp "$CPUS",cores=2,sockets=1
    -device qemu-xhci,id=xhci -device usb-kbd,bus=xhci.0 -device usb-tablet,bus=xhci.0
    -device isa-applesmc,osk="$OSK"
    -drive if=pflash,format=raw,readonly=on,file="$OVMF_CODE"
    -drive if=pflash,format=raw,file="$OVMF_VARS"
    -smbios type=2
    -device ich9-ahci,id=sata
    -drive id=OpenCoreBoot,if=none,snapshot=on,format=qcow2,file="$OPENCORE_QCOW2"
    -device ide-hd,bus=sata.2,drive=OpenCoreBoot
    -drive id=MacHDD,if=none,file="$QCOW2_NAME",format=qcow2,discard=unmap,detect-zeroes=unmap
    -device ide-hd,bus=sata.4,drive=MacHDD
    -netdev user,id=net0,hostfwd=tcp::"$SSH_PORT"-:22
    -device virtio-net-pci,netdev=net0,id=net0,mac=52:54:00:c9:18:27
    -device vmware-svga
    -display none -vnc 127.0.0.1:"$VNC_DISP"
    -monitor unix:"$MON_SOCK",server,nowait
    -qmp unix:"$QMP_SOCK",server,nowait
    -daemonize -pidfile "$WORKDIR/qemu.pid"
  )
  if [[ -f BaseSystem.img ]]; then  # install stage only; setup/boot of an installed image omits it
    args+=(-drive id=InstallMedia,if=none,file=BaseSystem.img,format=raw -device ide-hd,bus=sata.3,drive=InstallMedia)
  fi
  qemu-system-x86_64 "${args[@]}"
  QEMU_PID="$(cat "$WORKDIR/qemu.pid")"
  log "QEMU pid $QEMU_PID"
}

stage_boot() {  # M1: capture screenshots over time to prove OpenCore -> Recovery boot
  local i
  for ((i = 1; i <= BOOT_SCREENSHOT_MINS; i++)); do
    sleep 60
    screendump "boot-${i}min"
    kill -0 "$QEMU_PID" 2>/dev/null || { log "QEMU exited early"; return 1; }
  done
  log "M1 boot stage done — inspect artifacts/boot-*.png for the Recovery screen"
}

boot_to_recovery() {  # OpenCore picker -> macOS Base System -> Recovery window
  sleep 75
  screendump "inst-00-picker"
  log "selecting macOS Base System (1x right) + booting (repeated ret for reliability)"
  mon "sendkey right"; sleep 2
  local t
  for t in 1 2 3 4 5; do mon "sendkey ret"; sleep 8; done
  sleep 150
  screendump "inst-01-recovery"
}


erase_target() {  # open Terminal (Shift-Cmd-T), erase disk0 as APFS "Macintosh"
  log "erasing disk0 via Terminal"
  chord shift meta_l t; sleep 4
  typestr "diskutil eraseDisk APFS Macintosh disk0"; keys ret; sleep 30
  screendump "ri-00-erased"
  chord meta_l q; sleep 3   # quit Terminal -> back to Recovery chooser
}

drive_installer() {  # adaptive: OCR the current installer pane each round, click the right control, until install starts.
  # Fixed sleeps fail across versions — Sequoia's "Loading Installation Information..." is far slower than Tahoe's,
  # so blind clicks fire while still on the intro. This loop waits per-pane instead.
  local round txt reached_disk=0
  for ((round = 1; round <= 40; round++)); do
    screendump "oc-$(printf '%02d' "$round")"
    txt="$(ocrtext)"
    log "installer round $round: $(printf '%s' "$txt" | head -c 100)"
    if printf '%s' "$txt" | grep -qiE "remaining|Installing macOS"; then
      log "install started at round $round"; return 0
    elif printf '%s' "$txt" | grep -qiE "Select the disk|where you want to install|Show All Disk|Macintosh"; then
      log "round $round: disk-select -> Macintosh + Continue (blue default, keys ret backup)"
      reached_disk=1
      ocrclick Macintosh; sleep 2; ocrclick Continue; sleep 2; keys ret; sleep 8
    elif printf '%s' "$txt" | grep -qiE "Disagree|have read|must agree|agree to the|license agreement"; then
      # OCR of the license/SLA body is unreliable frame-to-frame, but "Disagree" reads cleanly and appears
      # only here. agreebtn clicks the bottom Agree (opens the confirm sheet); modalagree clicks the centered
      # sheet Agree (OCR-unreadable, position-based). Doing both each round handles either pane.
      log "round $round: license/SLA (Disagree present) -> agreebtn opens sheet + modalagree confirms"
      agreebtn; sleep 3; modalagree; sleep 3
    elif printf '%s' "$txt" | grep -qiE "Loading Installation"; then
      log "round $round: still loading installation information, waiting"; sleep 12
    elif printf '%s' "$txt" | grep -qiE "set up the installation|click Continue"; then
      # keys ret backs up the OCR click: the intro Continue is the sole blue/default button, and its
      # label OCRs at borderline confidence (~16 on Sequoia, ~45 on Tahoe) that can fall under the
      # conf>=30 cutoff. A stray ret after the pane advances is a no-op (the SLA pane has no default).
      log "round $round: intro -> Continue (blue default, keys ret backup)"; ocrclick Continue; sleep 2; keys ret; sleep 4
    else
      log "round $round: unrecognized pane, waiting"; sleep 10
    fi
  done
  # Ran out of rounds without seeing "remaining". If we reached disk-select, the install is likely running
  # (progress-screen OCR is flaky) — let the monitor loop observe. If we never got past intro/license, it's
  # genuinely hung, so fail fast and skip the 55-min monitor.
  if [ "$reached_disk" = 1 ]; then
    log "drive_installer: reached disk-select but never OCR'd progress; assuming install running"; return 0
  fi
  log "drive_installer: hung before disk-select after $round rounds"; return 1
}

stage_install() {  # M2: erase target, then adaptively drive the GUI installer to a real install
  boot_to_recovery
  erase_target            # Terminal: erase disk0 -> APFS "Macintosh"; back at chooser
  log "Reinstall macOS $MACOS_SHORTNAME (enter installer, then adaptive OCR click-through)"
  ocrclick Reinstall; sleep 1; ocrclick Continue; sleep 6
  drive_installer || { log "click-through hung before disk-select — skipping the 55-min monitor + capture"; return 1; }
  screendump "oc-installing"
  log "monitoring install + jiggling mouse to defeat macOS display-sleep (black screen after ~34min was sleep)"
  local i
  for ((i = 1; i <= 55; i++)); do
    python3 "$QMP_PY" "$QMP_SOCK" move $((420 + i % 180)) 420 2>/dev/null || true  # jiggle: keep display awake
    sleep 60
    screendump "oc-mon-$(printf '%02d' "$i")"
    kill -0 "$QEMU_PID" 2>/dev/null || { log "QEMU exited at install monitor $i"; return 1; }
  done
  log "install monitor done — inspect oc-mon-*.png for completion + Setup Assistant (display kept awake)"
  capture_and_push "$GHCR_TAG-base"
}

capture_and_push() {  # stop QEMU, compress the installed macOS qcow2, push it to ghcr as a reusable base
  local tag="$1"
  log "stopping QEMU + capturing installed macOS qcow2"
  mon "quit"; sleep 10
  [[ -f "$WORKDIR/qemu.pid" ]] && kill "$(cat "$WORKDIR/qemu.pid")" 2>/dev/null || true
  sleep 3
  # capture to WORKDIR (NOT ARTIFACT_DIR) so the ~10GB image isn't uploaded as a CI artifact
  local out="$WORKDIR/$QCOW2_NAME"
  qemu-img convert -O qcow2 -c "$OSX_KVM_DIR/$QCOW2_NAME" "$out"
  log "captured $(du -h "$out" | cut -f1); pushing -> $GHCR_REPO:$tag"
  if command -v oras >/dev/null 2>&1; then
    ( cd "$WORKDIR" && oras push "$GHCR_REPO:$tag" \
        --artifact-type application/vnd.cocoon.macos.disk \
        "$QCOW2_NAME:application/octet-stream" ) || log "oras push failed (check ghcr perms)"
  else
    log "oras not installed; skipping push"
  fi
}

pull_image() {  # oras pull $GHCR_REPO:$1 -> $QCOW2_NAME (the boot disk; skips the ~50min install)
  cd "$OSX_KVM_DIR"
  log "pulling image $GHCR_REPO:$1"
  # retry: ghcr streams multi-GB base blobs over HTTP/2 and intermittently drops them with
  # PROTOCOL_ERROR/stream errors, which is transient — a fresh pull almost always succeeds.
  local attempt
  for attempt in 1 2 3; do
    oras pull "$GHCR_REPO:$1" -o . && break
    log "oras pull attempt $attempt failed; retrying in $((attempt * 10))s"
    sleep $((attempt * 10))
  done
  [[ -f "$QCOW2_NAME" ]] || { log "FATAL: image $QCOW2_NAME not pulled from :$1 after 3 attempts"; exit 1; }
  log "image present: $(du -h "$QCOW2_NAME" | cut -f1)"
}

boot_installed() {  # OpenCore picker -> installed macOS (2nd entry, right of EFI) -> Setup Assistant
  sleep 75
  screendump "setup-00-picker"
  log "booting installed macOS (1x right + repeated ret)"
  mon "sendkey right"; sleep 2
  local t
  for t in 1 2 3 4 5; do mon "sendkey ret"; sleep 8; done
}

PROVISION_URL="https://raw.githubusercontent.com/cocoonstack/cocoon-macos/master/scripts/provision-macos.sh"

stage_setup() {  # M2b: boot Recovery (keyboard works there) + provision the installed volume (skip GUI Setup Assistant)
  sleep 75
  screendump "su-00-picker"
  log "booting macOS Base System (recovery) at the picker"
  mon "sendkey right"; sleep 2
  local t
  for t in 1 2 3 4 5; do mon "sendkey ret"; sleep 8; done
  log "waiting for Recovery (jiggle to keep display awake)"
  local w
  for w in 1 2 3 4 5; do python3 "$QMP_PY" "$QMP_SOCK" move $((60 + w * 25)) 400 2>/dev/null || true; sleep 25; done
  screendump "su-01-recovery"
  log "opening Terminal (Shift-Cmd-T) + running provision script via curl|bash"
  chord shift meta_l t; sleep 5; screendump "su-02-terminal"
  typestr "curl -fsSL $PROVISION_URL -o /tmp/p.sh && bash /tmp/p.sh"; keys ret
  local i
  for ((i = 1; i <= 8; i++)); do
    sleep 12; screendump "su-03-provision-$(printf '%02d' "$i")"
    kill -0 "$QEMU_PID" 2>/dev/null || { log "QEMU exited"; return 1; }
  done
  log "provision done; rebooting into the installed OS to settle its first-boot finalization"
  mon "quit"; sleep 10
  [[ -f "$WORKDIR/qemu.pid" ]] && kill "$(cat "$WORKDIR/qemu.pid")" 2>/dev/null || true
  sleep 3
  # provisioning ran offline from Recovery, so the installed OS never did its one-time first-full-boot
  # finalization (kernelcache/dyld rebuild, LaunchServices registration — a ~40min CPU/disk storm).
  # Boot it here, once, at build time and bake a SETTLED image, so consumers never pay that tax.
  configure_opencore hide   # installed macOS becomes the sole/default picker entry
  launch_qemu
  absorb_finalization_and_capture "$GHCR_TAG"   # tahoe:26 = SA-skipped, user 'cocoon', SSH-ready, settled
}

# absorb_finalization_and_capture boots the attached MacHDD, waits through the one-time first-full-boot
# finalization over SSH, bakes the perf recipe, slims free space, cleanly shuts down, and captures+pushes
# the settled image as $1. Shared by stage_setup (initial bake) and stage_slim (re-slim a published tag).
absorb_finalization_and_capture() {
  local tag="$1"
  boot_macintosh
  log "waiting for SSH (first full boot runs the one-time 'completing installation' finalization)"
  # ~40min CPU-pegged even on bare-metal Zen5 (Spotlight now excluded offline, but kernelcache/dyld
  # rebuild is irreducible); the nested-KVM GHA runner is slower, so budget ~100min.
  wait_ssh 300 || { log "FATAL: SSH never came up"; return 1; }
  apply_perf_recipe   # bake the macOS-side perf tweaks into the image before reclaiming/repushing
  slim_disk
  log "clean shutdown"
  gssh 'echo cocoon | sudo -S shutdown -h now' >/dev/null 2>&1 || true
  sleep 20
  capture_and_push "$tag"   # convert -c drops the now-zeroed/unmapped stale clusters
}

picker_size() { stat -f%z "$ARTIFACT_DIR/$1.png" 2>/dev/null || stat -c%s "$ARTIFACT_DIR/$1.png" 2>/dev/null || echo 0; }

boot_macintosh() {  # leave the OpenCore picker into the installed macOS: prefer auto-boot, else sendkey (retry ret)
  sleep 50
  screendump "vf-00"
  if [ "$(picker_size vf-00)" -le 30000 ]; then
    log "OpenCore auto-booted (frame $(picker_size vf-00) bytes)"
    return 0
  fi
  # Still at the picker. With HideAuxiliary the installed macOS is the sole/default entry, so a bare
  # ret boots it — do NOT lead with 'right' (that moves selection OFF the only entry, which left Tahoe
  # stuck). And confirm we actually left: one sub-threshold frame can be a transient/black flash during
  # the keypress (Tahoe bounced straight back to the 35875-byte picker), so re-shoot after a settle.
  log "still at picker ($(picker_size vf-00) bytes); booting the default entry via sendkey ret"
  local a sz
  for a in $(seq 1 10); do
    mon "sendkey ret"; sleep 10
    screendump "vf-pk-$a"
    if [ "$(picker_size vf-pk-$a)" -le 30000 ]; then
      sleep 10; screendump "vf-pk-${a}c"
      sz=$(picker_size "vf-pk-${a}c")
      [ "$sz" -le 30000 ] && { log "left picker after $a ret (confirmed $sz bytes)"; return 0; }
      log "picker bounced back after $a ret ($sz bytes); retrying"
    fi
    [ $((a % 4)) -eq 0 ] && { log "nudging selection (right) in case the default is not macOS"; mon "sendkey right"; sleep 2; }
  done
  log "WARN: still at the picker after 10 ret attempts"
}

stage_verify() {  # boot the turnkey tahoe:26 and confirm SSH
  boot_macintosh
  log "macOS booting; jiggling mouse (past the picker) to keep display awake; first-boot enables SSH"
  local w
  for w in $(seq 1 8); do python3 "$QMP_PY" "$QMP_SOCK" move $((60 + w * 20)) 400 2>/dev/null || true; sleep 20; done
  screendump "vf-01-loginscreen"
  log "testing SSH cocoon@localhost:$SSH_PORT"
  local ok=""
  for w in $(seq 1 14); do
    if sshpass -p cocoon ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=8 \
         -p "$SSH_PORT" cocoon@localhost 'sw_vers; hostname; id' >"$ARTIFACT_DIR/ssh-output.txt" 2>&1; then
      ok=1; log "SSH OK:"; cat "$ARTIFACT_DIR/ssh-output.txt"; break
    fi
    log "SSH attempt $w not ready; waiting"; sleep 25
    python3 "$QMP_PY" "$QMP_SOCK" move $((60 + w * 15)) 420 2>/dev/null || true
  done
  screendump "vf-03-final"
  [[ -n "$ok" ]] && log "VERIFY PASS: turnkey macOS boots + SSH works" || log "VERIFY: SSH not reachable yet (inspect vf-*.png + ssh-output.txt)"
}

gssh() { sshpass -p cocoon ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=8 -p "$SSH_PORT" cocoon@localhost "$@"; }

wait_ssh() {  # poll guest SSH (jiggling the mouse to keep the display awake); returns 0 once it answers
  local w
  for w in $(seq 1 "${1:-16}"); do
    gssh true 2>/dev/null && return 0
    sleep 20
    python3 "$QMP_PY" "$QMP_SOCK" move $((60 + w * 13)) 400 2>/dev/null || true
    [ $((w % 4)) -eq 0 ] && screendump "wait-$(printf '%02d' "$w")"  # boot-timeline frames (uploaded even on failure)
  done
  return 1
}

apply_desktop_recipe() {  # skip Setup Assistant + auto-login cocoon + no display sleep + suppress keyboard wizard
  log "applying boot-to-desktop recipe over SSH"
  gssh 'bash -s' <<'GUEST'
PV=$(sw_vers -productVersion); BV=$(sw_vers -buildVersion); echo "[recipe] PV=$PV BV=$BV"
echo cocoon | sudo -S createhomedir -c -u cocoon >/dev/null 2>&1   # cocoon has never GUI-logged-in
P=/Users/cocoon/Library/Preferences/com.apple.SetupAssistant
for k in DidSeeCloudSetup DidSeeSiriSetup DidSeePrivacy DidSeeScreenTime DidSeeAppearanceSetup DidSeeTouchIDSetup DidSeeApplePaySetup DidSeeActivationLock DidSeeAccessibility DidSeeAvatarSetup DidSeeSyncSetup DidSeeSyncSetup2 DidSeeTermsOfAddress DidSeeLockdownMode DidSeeAppStore DidSeeiCloudLoginForStorageServices; do echo cocoon | sudo -S defaults write $P "$k" -bool true; done
echo cocoon | sudo -S defaults write $P GestureMovieSeen none
echo cocoon | sudo -S defaults write $P LastSeenCloudProductVersion -string "$PV"
echo cocoon | sudo -S defaults write $P LastSeenBuddyBuildVersion -string "$BV"   # MUST be the build, or the cloud pane respawns
echo cocoon | sudo -S defaults write $P LastSeenDiagnosticsProductVersion -string "$PV"
echo cocoon | sudo -S defaults write $P LastSeenSiriProductVersion -string "$PV"
echo cocoon | sudo -S defaults write $P LastSeenSyncProductVersion -string "$PV"
echo cocoon | sudo -S defaults write $P MiniBuddyShouldLaunchToResumeSetup -bool false
echo cocoon | sudo -S defaults write $P MiniBuddyLaunchReason -integer 0
echo cocoon | sudo -S chown -R cocoon:staff /Users/cocoon/Library/Preferences
echo cocoon | sudo -S defaults write /Library/Preferences/com.apple.loginwindow autoLoginUser -string cocoon
# kcpassword bytes for "cocoon" (XOR Apple key, padded to 12). guest python3 is the CLT stub and
# macOS /bin/bash is 3.2 (no printf \xHH), so write the raw bytes with perl pack.
echo cocoon | sudo -S bash -c 'perl -e "print pack(q{C*},30,230,49,76,189,210,221,234,163,185,31,125)" > /etc/kcpassword; chmod 600 /etc/kcpassword; chown root:wheel /etc/kcpassword'
# suppress the Keyboard Setup Assistant for the QEMU USB keyboard (idVendor 1575, idProduct 1) -> ANSI(40)
echo cocoon | sudo -S defaults write /Library/Preferences/com.apple.keyboardtype keyboardtype -dict-add "1-1575-0" -int 40
echo cocoon | sudo -S pmset -a displaysleep 0 sleep 0 disksleep 0
echo "[recipe] autoLoginUser=$(echo cocoon | sudo -S defaults read /Library/Preferences/com.apple.loginwindow autoLoginUser 2>/dev/null) kcpw=$(echo cocoon | sudo -S stat -f %z /etc/kcpassword 2>/dev/null)b"
echo RECIPE_OK
GUEST
}

slim_disk() {  # reclaim stale qcow2 clusters: drop sleepimage/caches, then zero-fill free space (detect-zeroes=unmap drops it in place)
  log "slimming: drop sleepimage + caches, zero-fill free space"
  gssh 'bash -s' <<'GUEST'
echo cocoon | sudo -S pmset -a hibernatemode 0 2>/dev/null
echo cocoon | sudo -S rm -f /private/var/vm/sleepimage 2>/dev/null
echo cocoon | sudo -S rm -rf /Library/Caches/* /System/Library/Caches/* /private/var/log/* 2>/dev/null
rm -rf ~/Library/Caches/* ~/.Trash/* 2>/dev/null
echo "[slim] used before zero-fill: $(df -h / | tail -1 | awk '{print $3}')"
echo cocoon | sudo -S dd if=/dev/zero of=/private/var/tmp/ZZ bs=1048576 2>/dev/null; echo cocoon | sudo -S rm -f /private/var/tmp/ZZ; sync; sleep 3
echo SLIM_OK
GUEST
}

apply_perf_recipe() {  # macOS-side perf, baked over SSH during slim so an SSH-ready image re-bakes fast
  # (no full `setup` re-run): kill the post-boot Spotlight/agent storm + cut GUI compositor work on the
  # unaccelerated vmware-svga framebuffer. Applies to whichever macOS this stage booted (tahoe|sequoia).
  log "applying perf recipe (Spotlight off, trim agents, reduce-motion) over SSH"
  gssh 'bash -s' <<'GUEST'
echo cocoon | sudo -S /usr/bin/mdutil -a -i off 2>/dev/null || true   # disable Spotlight indexing (the post-boot churn)
echo cocoon | sudo -S /usr/bin/mdutil -a -d   2>/dev/null || true
for svc in com.apple.photoanalysisd com.apple.mediaanalysisd com.apple.parsecd com.apple.suggestd; do
  launchctl disable "gui/501/$svc" 2>/dev/null || true
done
defaults write com.apple.universalaccess reduceTransparency -bool true 2>/dev/null || true
defaults write com.apple.universalaccess reduceMotion -bool true 2>/dev/null || true
defaults write -g NSAutomaticWindowAnimationsEnabled -bool false 2>/dev/null || true
defaults write com.apple.dock launchanim -bool false 2>/dev/null || true
defaults write com.apple.dock expose-animation-duration -float 0 2>/dev/null || true
killall cfprefsd 2>/dev/null || true   # flush prefs to disk so they survive the upcoming clean shutdown
echo "PERF_OK spotlight=$(mdutil -a -s 2>/dev/null | head -1)"
GUEST
}

# WIP — BLOCKED on the macOS 26 system Setup Assistant. A fresh :26 boots to the system SA
# (_mbsetupuser / SetupAssistantSpringboard) and it resists every marker-based skip we tried
# (.AppleSetupDone, complete home, .skipbuddy, DidSee*, autologin, killsa, rm ConfigurationProfiles).
# So this stage's recipe never runs to completion: console stays _mbsetupuser after the reboot.
# The recipe itself (apply_desktop_recipe) IS validated post-SA. To finish this, the system SA must
# be clicked through with the mouse/OCR (keyboard does not register at SA) before the recipe applies.
stage_desktop() {  # turn the SSH-ready :26 into a boot-straight-to-desktop + slimmed image, then re-push :26
  boot_macintosh
  log "waiting for SSH on the SSH-ready image (complete-home first boot is slow, ~10min)"
  wait_ssh 36 || { log "FATAL: SSH never came up"; return 1; }
  apply_desktop_recipe
  log "rebooting so the next boot auto-logs into cocoon's desktop"
  gssh 'echo cocoon | sudo -S reboot' >/dev/null 2>&1 || true   # SSH drops on reboot -> exit 255; do not let set -e abort
  sleep 40
  boot_macintosh
  wait_ssh 36 || { log "FATAL: SSH not back after reboot"; return 1; }
  local cu="" w
  for w in $(seq 1 24); do cu=$(gssh 'stat -f %Su /dev/console' 2>/dev/null || true); [ "$cu" = cocoon ] && break; sleep 15; done
  python3 "$QMP_PY" "$QMP_SOCK" move 220 220 2>/dev/null || true; sleep 2
  screendump "desktop-01"
  log "console user after reboot: $cu"
  [ "$cu" = cocoon ] || { log "FATAL: did not auto-login to cocoon desktop (console=$cu)"; return 1; }
  log "=== DESKTOP OK: auto-logged into cocoon's desktop ==="
  slim_disk
  log "clean shutdown"
  gssh 'echo cocoon | sudo -S shutdown -h now' >/dev/null 2>&1 || true   # SSH drops on shutdown -> exit 255
  sleep 20
  capture_and_push "$GHCR_TAG"   # tahoe:26 = boots straight to cocoon's desktop, slimmed
}

stage_slim() {  # re-slim an already-published tag: boot, reclaim stale clusters over SSH, re-push.
  # Now that stage_setup bakes a settled image, this is a re-run knob (e.g. to re-slim after changes);
  # it shares the same boot-through-finalization + slim + clean-shutdown + capture path.
  absorb_finalization_and_capture "$GHCR_TAG"
}

main() {
  require_kvm
  setup_osx_kvm
  prepare_firmware   # bake LongQT OpenCore + stage OVMF_VARS before any launch/configure_opencore
  case "$STAGE" in
    boot)
      fetch_recovery; launch_qemu; stage_boot ;;
    install)
      fetch_recovery; configure_opencore; launch_qemu; stage_install ;;
    setup)
      pull_image "$GHCR_TAG-base"; fetch_recovery; launch_qemu; stage_setup ;;
    desktop)
      pull_image "$GHCR_TAG"; configure_opencore hide; launch_qemu; stage_desktop ;;
    verify)
      pull_image "$GHCR_TAG"; configure_opencore hide; launch_qemu; stage_verify ;;
    slim)
      pull_image "$GHCR_TAG"; configure_opencore hide; launch_qemu; stage_slim ;;
    *)
      log "unknown STAGE=$STAGE"; exit 2 ;;
  esac
  log "done (stage=$STAGE)"
}

main "$@"
