#!/usr/bin/env bash
# cocoon-macos firmware library — bakes the validated LongQT OpenCore loader into a bootable
# GPT+ESP qcow2. Sourced by both scripts/doctor.sh (host provisioning) and scripts/build-qemu-macos.sh
# (CI image build) so both boot the exact same OpenCore stack. Override the loader version with
# LONGQT_VER=vX.Y.
#
# Callers must provide a `log` function and be able to run qemu-img/qemu-nbd/sgdisk/mkfs.fat/curl plus
# load the nbd module. Privileged steps go through $SUDO, so this works whether the caller is already
# root (doctor) or an unprivileged user with passwordless sudo (the CI runner).

LONGQT_VER="${LONGQT_VER:-v0.7}"

# doctor runs as root (SUDO empty, identical to its previous inline behavior); the CI build runs
# unprivileged and needs sudo for the nbd/mount/mkfs steps.
if [ "$(id -u)" = 0 ]; then SUDO=""; else SUDO="sudo"; fi

# bake_opencore_qcow2 OUT_QCOW2 — download the LongQT OpenCore ISO and copy its EFI onto a fresh
# GPT+ESP FAT32 qcow2 at OUT_QCOW2.
#
# OpenCore must be a writable disk so a per-VM overlay can inject a unique SMBIOS; the LongQT loader
# ships only as an ISO, so copy its EFI onto a fresh ESP. Runs the whole bake in a subshell so its
# EXIT-trap cleanup is scoped here and never clobbers the caller's own trap.
bake_opencore_qcow2() {
	local out="$1"
	local url="https://github.com/LongQT-sea/OpenCore-ISO/releases/download/${LONGQT_VER}/LongQT-OpenCore-${LONGQT_VER}.iso"
	$SUDO modprobe nbd max_part=8 2>/dev/null || true
	(
		local tmp nbd="" esp="" iso=""
		tmp="$(mktemp -d)"
		# shellcheck disable=SC2329  # invoked indirectly via the trap below
		cleanup() {
			[ -n "$esp" ] && $SUDO umount "$esp" 2>/dev/null || true
			[ -n "$iso" ] && $SUDO umount "$iso" 2>/dev/null || true
			[ -n "$nbd" ] && $SUDO qemu-nbd --disconnect "$nbd" >/dev/null 2>&1 || true
			rm -rf "$tmp"
		}
		trap cleanup EXIT

		log "downloading LongQT OpenCore $LONGQT_VER"
		curl -fsSL -o "$tmp/oc.iso" "$url"

		log "baking OpenCore.qcow2"
		qemu-img create -f qcow2 "$out" 384M >/dev/null
		local i
		for i in $(seq 0 15); do
			if [ -e "/dev/nbd$i" ] && [ ! -e "/sys/block/nbd$i/pid" ]; then nbd="/dev/nbd$i"; break; fi
		done
		[ -n "$nbd" ] || { echo "no free /dev/nbd device" >&2; exit 1; }
		$SUDO qemu-nbd --connect="$nbd" -f qcow2 "$out"
		# qemu-nbd registers the block device asynchronously; wait for its size before partitioning.
		for _ in $(seq 50); do [ "$($SUDO blockdev --getsize64 "$nbd" 2>/dev/null || echo 0)" -gt 0 ] && break; sleep 0.1; done
		$SUDO sgdisk -Z "$nbd" >/dev/null
		$SUDO sgdisk -n 1:0:0 -t 1:ef00 -c 1:EFI "$nbd" >/dev/null
		$SUDO partprobe "$nbd" 2>/dev/null || true
		for _ in $(seq 50); do [ -e "${nbd}p1" ] && break; $SUDO partprobe "$nbd" 2>/dev/null || true; sleep 0.1; done
		$SUDO mkfs.fat -F32 "${nbd}p1" >/dev/null
		esp="$tmp/esp" iso="$tmp/iso"
		mkdir -p "$esp" "$iso"
		$SUDO mount "${nbd}p1" "$esp"
		$SUDO mount -o loop,ro "$tmp/oc.iso" "$iso"
		$SUDO cp -r "$iso/EFI_RELEASE/EFI" "$esp/"
		sync
	)
}
