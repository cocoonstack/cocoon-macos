#!/usr/bin/env bash
# cocoon-macos doctor — make a host ready to run macOS VMs, idempotently.
#
# Checks /dev/kvm and the qemu/nbd/ovmf deps (installing what is missing), then provisions the shared
# OpenCore + OVMF firmware into <state-dir>/firmware — one copy, reused by every VM, like cocoon's
# CLOUDHV.fd. You never choose "OpenCore vs LongQT": the firmware is the validated LongQT OpenCore,
# which boots macOS identically on Intel and AMD hosts.
#
# Re-run any time; it skips work already done. Override the loader version with LONGQT_VER=vX.Y.
set -euo pipefail

STATE_DIR="${COCOON_MACOS_HOME:-/var/lib/cocoon-macos}"
FW="$STATE_DIR/firmware"

log() { printf '\033[1;36m==>\033[0m %s\n' "$*"; }
die() { printf '\033[1;31mFAIL:\033[0m %s\n' "$*" >&2; exit 1; }

# shellcheck source=scripts/lib-firmware.sh
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/lib-firmware.sh"

[ "$(id -u)" = 0 ] || die "run as root: sudo scripts/doctor.sh (it installs packages and writes $STATE_DIR)"

log "checking /dev/kvm"
[ -e /dev/kvm ] || die "/dev/kvm missing — enable hardware virtualization (nested virt on a cloud VM)"

log "checking host packages"
missing=()
command -v qemu-system-x86_64 >/dev/null || missing+=(qemu-system-x86)
command -v qemu-img >/dev/null || missing+=(qemu-utils)
command -v sgdisk >/dev/null || missing+=(gdisk)
command -v mkfs.fat >/dev/null || missing+=(dosfstools)
command -v curl >/dev/null || missing+=(curl)
[ -f /usr/share/OVMF/OVMF_CODE_4M.fd ] || missing+=(ovmf)
if [ ${#missing[@]} -gt 0 ]; then
	command -v apt-get >/dev/null || die "missing ${missing[*]} and no apt-get — install them manually"
	log "installing: ${missing[*]}"
	apt-get update -qq
	apt-get install -y -qq "${missing[@]}"
fi

log "loading nbd module"
modprobe nbd max_part=8 2>/dev/null || true
[ -e /dev/nbd0 ] || die "no /dev/nbd device — the nbd kernel module is required (CONFIG_BLK_DEV_NBD)"

mkdir -p "$FW"
if [ -f "$FW/OpenCore.qcow2" ] && [ -f "$FW/OVMF_CODE.fd" ] && [ -f "$FW/OVMF_VARS.fd" ]; then
	log "firmware already provisioned in $FW"
	log "✅ host ready — run: cocoon-macos vm run <image> --net cni --random-smbios"
	exit 0
fi

[ -f /usr/share/OVMF/OVMF_VARS_4M.fd ] || die "OVMF_VARS_4M.fd missing after installing ovmf (unexpected OVMF layout)"
log "installing OVMF (4M build)"
cp -f /usr/share/OVMF/OVMF_CODE_4M.fd "$FW/OVMF_CODE.fd"
cp -f /usr/share/OVMF/OVMF_VARS_4M.fd "$FW/OVMF_VARS.fd"

bake_opencore_qcow2 "$FW/OpenCore.qcow2"

log "✅ host ready — firmware in $FW"
log "next: cocoon-macos image pull ghcr.io/cocoonstack/cocoon-macos/tahoe:26"
log "      cocoon-macos vm run ghcr.io/cocoonstack/cocoon-macos/tahoe:26 --net cni --random-smbios"
