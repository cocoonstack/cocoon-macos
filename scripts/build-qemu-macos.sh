#!/usr/bin/env bash
# Build a golden macOS (Tahoe 26) qcow2 on an x86 Linux/KVM host and push it to
# ghcr. Modeled on cocoonstack/windows scripts/build-qemu.sh, but macOS has no
# autounattend equivalent — the install step (automate_install) is the R&D spike.
set -euo pipefail

ROOT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
WORKDIR=${WORKDIR:-"$ROOT_DIR/work/qemu-build"}
ARTIFACT_DIR=${ARTIFACT_DIR:-"$WORKDIR/artifacts"}

MACOS_VERSION=${MACOS_VERSION:-"tahoe"}     # OSX-KVM fetch-macOS-v2.py product
QCOW2_NAME=${QCOW2_NAME:-"macos-tahoe-26.qcow2"}
DISK_SIZE=${DISK_SIZE:-80G}
CPUS=${CPUS:-4}
MEMORY=${MEMORY:-8G}
SSH_PORT=${SSH_PORT:-2222}
VNC_DISP=${VNC_DISP:-1}
QMP_SOCK="$WORKDIR/qmp.sock"
GHCR_REPO=${GHCR_REPO:-"ghcr.io/cocoonstack/cocoon-macos/tahoe"}
GHCR_TAG=${GHCR_TAG:-"26"}

# OpenCore EFI for Tahoe — vendored from LongQT-sea/OpenCore-ISO (the Tahoe upstream of record).
OPENCORE_IMG=${OPENCORE_IMG:-"$ROOT_DIR/os-image/tahoe/OpenCore.qcow2"}
OVMF_CODE=${OVMF_CODE:-"/usr/share/OVMF/OVMF_CODE.fd"}
OVMF_VARS_SRC=${OVMF_VARS_SRC:-"/usr/share/OVMF/OVMF_VARS.fd"}

QEMU_PID=""
mkdir -p "$WORKDIR" "$ARTIFACT_DIR"

log() { printf '[build-macos] %s\n' "$*"; }
cleanup() { set +e; [[ -n "$QEMU_PID" ]] && { kill "$QEMU_PID" 2>/dev/null; wait "$QEMU_PID" 2>/dev/null; }; }
trap cleanup EXIT

require_kvm() {
  [[ -e /dev/kvm ]] || { log "FATAL: /dev/kvm missing — need a host with KVM (incl. GitHub ubuntu-latest)"; exit 1; }
  log "KVM present: $(ls -l /dev/kvm)"
}

fetch_recovery() {
  # OSX-KVM fetch-macOS-v2.py pulls the recovery dmg from Apple's CDN, then BaseSystem.img.
  log "fetching macOS $MACOS_VERSION recovery from Apple CDN"
  # TODO(P0): vendor OSX-KVM's fetch-macOS-v2.py and call:
  #   python3 fetch-macOS-v2.py --action download --board-id <Tahoe> -o "$WORKDIR/BaseSystem.dmg"
  #   dmg2img "$WORKDIR/BaseSystem.dmg" "$WORKDIR/BaseSystem.img"
  :
}

prepare_disk() {
  log "creating blank install target qcow2 ($DISK_SIZE)"
  qemu-img create -f qcow2 "$WORKDIR/$QCOW2_NAME" "$DISK_SIZE"
  cp "$OVMF_VARS_SRC" "$WORKDIR/OVMF_VARS.fd"
}

launch_qemu() {
  log "launching QEMU (headless, VNC :$VNC_DISP, ssh :$SSH_PORT, QMP $QMP_SOCK)"
  # TODO(P0/P1): finalize the arg vector (mirror qemu/launch.go Spec.Args):
  #   -machine q35,accel=kvm -cpu Skylake-Client-v4,vendor=GenuineIntel,+invtsc
  #   -smp "$CPUS" -m "$MEMORY"
  #   -drive if=pflash,format=raw,readonly=on,file="$OVMF_CODE"
  #   -drive if=pflash,format=raw,file="$WORKDIR/OVMF_VARS.fd"
  #   -drive id=opencore,if=none,file="$OPENCORE_IMG",format=qcow2  -device ide-hd,bus=...,drive=opencore
  #   -drive id=basesystem,if=none,file="$WORKDIR/BaseSystem.img",format=raw -device ...
  #   -drive id=macos,if=none,file="$WORKDIR/$QCOW2_NAME",format=qcow2 -device virtio-blk-pci,drive=macos
  #   -device virtio-tablet-pci
  #   -netdev user,id=net0,hostfwd=tcp::"$SSH_PORT"-:22 -device virtio-net-pci,netdev=net0
  #   -vnc :"$VNC_DISP" -qmp unix:"$QMP_SOCK",server,nowait -daemonize
  :
}

automate_install() {
  # *** THE SPIKE (P0) ***  macOS has no autounattend.
  # Candidate A: drive Recovery's Disk Utility + installer via QMP `sendkey` + VNC-screenshot OCR.
  # Candidate B: open Recovery > Utilities > Terminal, then script:
  #     diskutil eraseDisk APFS Macintosh disk0
  #     "/Volumes/.../startosinstall" --agreetolicense --nointeraction --volume /Volumes/Macintosh
  #   driven keystroke-by-keystroke over QMP sendkey.
  # Candidate C (fallback, if P0 spike stalls): a one-time manually-built golden qcow2,
  #   committed/cached so CI only derives + provisions from it.
  log "TODO(P0): automate the macOS install — load-bearing spike, gates the whole pipeline"
  return 1
}

provision_via_ssh() {
  # After install + first boot, enable Remote Login and provision (cocoon-agent, etc.) over SSH.
  log "TODO(P1): wait for SSH on :$SSH_PORT, enable Remote Login, provision guest"
  :
}

capture_image() {
  log "compacting + capturing $QCOW2_NAME"
  qemu-img convert -O qcow2 -c "$WORKDIR/$QCOW2_NAME" "$ARTIFACT_DIR/$QCOW2_NAME"
}

push_ghcr() {
  log "pushing $ARTIFACT_DIR/$QCOW2_NAME -> $GHCR_REPO:$GHCR_TAG"
  # TODO(P3): align OCI layout with cocoon/epoch image pull. Likely:
  #   oras push "$GHCR_REPO:$GHCR_TAG" "$ARTIFACT_DIR/$QCOW2_NAME:application/vnd.cocoon.macos.qcow2"
  :
}

main() {
  require_kvm
  fetch_recovery
  prepare_disk
  launch_qemu
  automate_install
  provision_via_ssh
  capture_image
  push_ghcr
  log "done"
}

main "$@"
