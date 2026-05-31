#!/usr/bin/env bash
# Build a golden macOS (Tahoe 26) qcow2 on an x86 Linux/KVM host and push it to
# ghcr. Leans on kholia/OSX-KVM (which already integrates LongQT-sea's Tahoe
# OpenCore): it provides fetch-macOS-v2.py, OVMF, OpenCore.qcow2 and the OSK.
#
# STAGE controls how far we go (the macOS install has no autounattend, so it is
# the iterated R&D spike — driven via the Action with retries):
#   boot    — M1: boot headless to the macOS Recovery/installer, screenshot proof
#   install — M2: drive Recovery -> erase -> startosinstall headlessly (sendkey)
#   image   — M3: capture + push the golden qcow2 to ghcr
set -euo pipefail

STAGE=${STAGE:-boot}
ROOT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
WORKDIR=${WORKDIR:-"$ROOT_DIR/work/qemu-build"}
ARTIFACT_DIR=${ARTIFACT_DIR:-"$WORKDIR/artifacts"}
OSX_KVM_DIR="$WORKDIR/OSX-KVM"

OSX_KVM_REPO=${OSX_KVM_REPO:-"https://github.com/kholia/OSX-KVM.git"}
MACOS_SHORTNAME=${MACOS_SHORTNAME:-"tahoe"}   # fetch-macOS-v2.py --shortname (non-interactive; tahoe = macOS 26)
OSK="ourhardworkbythesewordsguardedpleasedontsteal(c)AppleComputerInc"

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

GHCR_REPO=${GHCR_REPO:-"ghcr.io/cocoonstack/cocoon-macos/tahoe"}
GHCR_TAG=${GHCR_TAG:-"26"}

QEMU_PID=""
mkdir -p "$WORKDIR" "$ARTIFACT_DIR"

log() { printf '[build-macos] %s\n' "$*"; }
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

screendump() {  # screendump <label>
  local ppm="$WORKDIR/$1.ppm" png="$ARTIFACT_DIR/$1.png"
  mon "screendump $ppm"
  sleep 1
  [[ -f "$ppm" ]] && { pnmtopng "$ppm" >"$png" 2>/dev/null || cp "$ppm" "$png"; rm -f "$ppm"; log "screenshot -> $1.png"; }
}

setup_osx_kvm() {
  [[ -d "$OSX_KVM_DIR" ]] || git clone --depth 1 "$OSX_KVM_REPO" "$OSX_KVM_DIR"
  python3 -m pip install --quiet --user requests click 2>/dev/null || true
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
  [[ -f OVMF_VARS.fd ]] || cp OVMF_VARS-1920x1080.fd OVMF_VARS.fd
}

launch_qemu() {
  cd "$OSX_KVM_DIR"
  log "launching QEMU headless (VNC 127.0.0.1:590$VNC_DISP, ssh :$SSH_PORT, monitor $MON_SOCK)"
  qemu-system-x86_64 \
    -enable-kvm -m "$MEMORY" \
    -cpu Skylake-Client,-hle,-rtm,kvm=on,vendor=GenuineIntel,+invtsc,vmware-cpuid-freq=on,+ssse3,+sse4.2,+popcnt,+avx,+aes,+xsave,+xsaveopt,check \
    -machine q35 \
    -smp "$CPUS",cores=2,sockets=1 \
    -device qemu-xhci,id=xhci -device usb-kbd,bus=xhci.0 -device usb-tablet,bus=xhci.0 \
    -device isa-applesmc,osk="$OSK" \
    -drive if=pflash,format=raw,readonly=on,file=OVMF_CODE_4M.fd \
    -drive if=pflash,format=raw,file=OVMF_VARS.fd \
    -smbios type=2 \
    -device ich9-ahci,id=sata \
    -drive id=OpenCoreBoot,if=none,snapshot=on,format=qcow2,file=OpenCore/OpenCore.qcow2 \
    -device ide-hd,bus=sata.2,drive=OpenCoreBoot \
    -drive id=InstallMedia,if=none,file=BaseSystem.img,format=raw \
    -device ide-hd,bus=sata.3,drive=InstallMedia \
    -drive id=MacHDD,if=none,file="$QCOW2_NAME",format=qcow2 \
    -device ide-hd,bus=sata.4,drive=MacHDD \
    -netdev user,id=net0,hostfwd=tcp::"$SSH_PORT"-:22 \
    -device virtio-net-pci,netdev=net0,id=net0,mac=52:54:00:c9:18:27 \
    -device vmware-svga \
    -display none -vnc 127.0.0.1:"$VNC_DISP" \
    -monitor unix:"$MON_SOCK",server,nowait \
    -qmp unix:"$QMP_SOCK",server,nowait \
    -daemonize -pidfile "$WORKDIR/qemu.pid"
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
  sleep 90
  screendump "inst-00-picker"
  log "booting 'macOS Base System' via OpenCanopy mouse double-click (HMP sendkey was flaky)"
  dclick 715 388        # macOS Base System icon (right entry); QMP pointer is reliable
  sleep 180
  screendump "inst-01-recovery"
}

open_disk_utility() {  # Recovery chooser -> Disk Utility -> Continue
  log "opening Disk Utility (click row + Continue)"
  click 600 465; sleep 1; click 815 525
  sleep 35
  screendump "du-00-open"
}

erase_target() {  # open Terminal (Shift-Cmd-T), erase disk0 as APFS "Macintosh"
  log "erasing disk0 via Terminal"
  chord shift meta_l t; sleep 4
  typestr "diskutil eraseDisk APFS Macintosh disk0"; keys ret; sleep 30
  screendump "ri-00-erased"
  chord meta_l q; sleep 3   # quit Terminal -> back to Recovery chooser
}

start_reinstall_to_license() {  # chooser -> Reinstall -> intro -> license
  click 600 280; sleep 1; click 815 525; sleep 6   # Reinstall row + Continue -> intro
  keys ret; sleep 5                                  # intro Continue (default) -> license
}

stage_install() {  # M2: recon Agree / confirm-sheet / disk-select coords (stop before the 15GB download)
  boot_to_recovery
  erase_target
  start_reinstall_to_license
  screendump "ri-10-license"
  log "click Agree on license"
  click 744 646; sleep 3; screendump "ri-11-confirm"     # license Agree -> confirm sheet
  log "click Agree on confirm sheet (guess)"
  click 744 360; sleep 5; screendump "ri-12-disksel"      # confirm Agree -> disk-select
  sleep 3; screendump "ri-13-disksel"
  log "recon done — inspect ri-1*.png for Agree/confirm/disk-select layout (download NOT started)"
}

stage_image() {  # M3
  log "compacting + capturing $QCOW2_NAME"
  qemu-img convert -O qcow2 -c "$OSX_KVM_DIR/$QCOW2_NAME" "$ARTIFACT_DIR/$QCOW2_NAME"
  log "TODO(M3): oras push $ARTIFACT_DIR/$QCOW2_NAME -> $GHCR_REPO:$GHCR_TAG"
}

main() {
  require_kvm
  setup_osx_kvm
  fetch_recovery
  launch_qemu
  case "$STAGE" in
    boot) stage_boot ;;
    install) stage_install ;;
    image) stage_image ;;
    *) log "unknown STAGE=$STAGE"; exit 2 ;;
  esac
  log "done (stage=$STAGE)"
}

main "$@"
