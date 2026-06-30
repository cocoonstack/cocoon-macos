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
LONGQT_VER="${LONGQT_VER:-v0.7}"
ISO_URL="https://github.com/LongQT-sea/OpenCore-ISO/releases/download/${LONGQT_VER}/LongQT-OpenCore-${LONGQT_VER}.iso"

log() { printf '\033[1;36m==>\033[0m %s\n' "$*"; }
die() { printf '\033[1;31mFAIL:\033[0m %s\n' "$*" >&2; exit 1; }

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

tmp="$(mktemp -d)"
nbd="" esp="" iso=""
cleanup() {
	[ -n "$esp" ] && umount "$esp" 2>/dev/null || true
	[ -n "$iso" ] && umount "$iso" 2>/dev/null || true
	[ -n "$nbd" ] && qemu-nbd --disconnect "$nbd" >/dev/null 2>&1 || true
	rm -rf "$tmp"
}
trap cleanup EXIT

log "downloading LongQT OpenCore $LONGQT_VER"
curl -fsSL -o "$tmp/oc.iso" "$ISO_URL"

# Bake the loader into a GPT+ESP qcow2: OpenCore must be a writable disk so a per-VM overlay can
# inject a unique SMBIOS; the LongQT loader ships only as an ISO, so copy its EFI onto a fresh ESP.
log "baking OpenCore.qcow2"
oc="$FW/OpenCore.qcow2"
qemu-img create -f qcow2 "$oc" 384M >/dev/null
for i in $(seq 0 15); do
	if [ -e "/dev/nbd$i" ] && [ ! -e "/sys/block/nbd$i/pid" ]; then nbd="/dev/nbd$i"; break; fi
done
[ -n "$nbd" ] || die "no free /dev/nbd device"
qemu-nbd --connect="$nbd" -f qcow2 "$oc"
# qemu-nbd registers the block device asynchronously; wait for its size before partitioning.
for _ in $(seq 50); do [ "$(blockdev --getsize64 "$nbd" 2>/dev/null || echo 0)" -gt 0 ] && break; sleep 0.1; done
sgdisk -Z "$nbd" >/dev/null
sgdisk -n 1:0:0 -t 1:ef00 -c 1:EFI "$nbd" >/dev/null
partprobe "$nbd" 2>/dev/null || true
for _ in $(seq 50); do [ -e "${nbd}p1" ] && break; partprobe "$nbd" 2>/dev/null || true; sleep 0.1; done
mkfs.fat -F32 "${nbd}p1" >/dev/null
esp="$tmp/esp" iso="$tmp/iso"
mkdir -p "$esp" "$iso"
mount "${nbd}p1" "$esp"
mount -o loop,ro "$tmp/oc.iso" "$iso"
cp -r "$iso/EFI_RELEASE/EFI" "$esp/"
sync
umount "$esp"
esp=""
umount "$iso"
iso=""
qemu-nbd --disconnect "$nbd"
nbd=""

log "✅ host ready — firmware in $FW"
log "next: cocoon-macos image pull ghcr.io/cocoonstack/cocoon-macos/tahoe:26"
log "      cocoon-macos vm run ghcr.io/cocoonstack/cocoon-macos/tahoe:26 --net cni --random-smbios"
