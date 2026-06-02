#!/usr/bin/env bash
# Build a golden macOS (Tahoe 26) qcow2 on an x86 Linux/KVM host and push it to
# ghcr. Leans on kholia/OSX-KVM (which already integrates LongQT-sea's Tahoe
# OpenCore): it provides fetch-macOS-v2.py, OVMF, OpenCore.qcow2 and the OSK.
#
# STAGE controls how far we go (the macOS install has no autounattend, so it is
# the iterated R&D spike — driven via the Action with retries):
# This is an IMAGE-only pipeline (no Go); the CLI is exercised separately on a testbed.
#   boot    — boot headless to the macOS Recovery/installer, screenshot proof
#   install — Recovery -> erase -> startosinstall headlessly, push tahoe:26-base
#   setup   — provision the installed image (create cocoon user + SSH), push tahoe:26
#   desktop — boot tahoe:26 -> auto-login cocoon + skip Setup Assistant + slim, re-push tahoe:26
#   verify  — boot tahoe:26 + confirm SSH
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
ocrclick() {  # WORD [ymin ymax]; wakes the display (neutral move) before OCR so it doesn't read a slept-black screen
  python3 "$QMP_PY" "$QMP_SOCK" move 60 400 2>/dev/null || true
  sleep 1
  python3 "$QMP_PY" "$QMP_SOCK" ocrclick "$@" 2>&1 | sed 's/^/[ocr] /' || true
}

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

configure_opencore() {  # patch OpenCore config.plist; arg "hide" => HideAuxiliary+short Timeout so it auto-boots the installed macOS
  local hide="${1:-}"
  local oc="$OSX_KVM_DIR/OpenCore/OpenCore.qcow2"
  [[ -f "$oc" ]] || { log "OpenCore.qcow2 missing; skip OC patch"; return 0; }
  log "patching OpenCore config.plist: RequestBootVarRouting=true + Timeout (qemu-nbd)"
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
      sudo HIDE="$hide" python3 - "$cfg" <<'PY' || true
import plistlib, os, sys
p = sys.argv[1]
d = plistlib.load(open(p, "rb"))
d.setdefault("Booter", {}).setdefault("Quirks", {})["RequestBootVarRouting"] = True
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
    -cpu Skylake-Client,-hle,-rtm,kvm=on,vendor=GenuineIntel,+invtsc,vmware-cpuid-freq=on,+ssse3,+sse4.2,+popcnt,+avx,+aes,+xsave,+xsaveopt,check
    -machine q35
    -smp "$CPUS",cores=2,sockets=1
    -device qemu-xhci,id=xhci -device usb-kbd,bus=xhci.0 -device usb-tablet,bus=xhci.0
    -device isa-applesmc,osk="$OSK"
    -drive if=pflash,format=raw,readonly=on,file=OVMF_CODE_4M.fd
    -drive if=pflash,format=raw,file=OVMF_VARS.fd
    -smbios type=2
    -device ich9-ahci,id=sata
    -drive id=OpenCoreBoot,if=none,snapshot=on,format=qcow2,file=OpenCore/OpenCore.qcow2
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

stage_install() {  # M2: OCR-driven Reinstall click-through, then start the install + observe
  boot_to_recovery
  erase_target            # Terminal: erase disk0 -> APFS "Macintosh"; back at chooser
  log "Reinstall macOS Tahoe (OCR + Return for blue default buttons)"
  ocrclick Reinstall; sleep 1; ocrclick Continue; sleep 6; screendump "oc-00-intro"
  keys ret; sleep 6; screendump "oc-01-license"                   # intro: blue 'Continue' = Return
  ocrclick Agree 600 720; sleep 4; screendump "oc-02-confirm"      # license Agree (dark theme)
  # confirm sheet (Disagree=blue default): try keyboard Tab->Agree then Space (position-independent),
  # then also the PIL pixel as backup.
  keys tab; sleep 1; keys spc; sleep 2; screendump "oc-02b-tab"
  click 676 399; sleep 6; screendump "oc-03-disksel"
  log "disk-select: pick Macintosh, then Continue (clean — no menu-bar misclick)"
  ocrclick Macintosh; sleep 2; ocrclick Continue 620 690; sleep 12
  screendump "oc-04-installing"
  log "monitoring install + jiggling mouse to defeat macOS display-sleep (black screen after ~34min was sleep)"
  local i
  for ((i = 1; i <= 55; i++)); do
    python3 "$QMP_PY" "$QMP_SOCK" move $((420 + i % 180)) 420 2>/dev/null || true  # jiggle: keep display awake
    sleep 60
    screendump "oc-05-$(printf '%02d' "$i")"
    kill -0 "$QEMU_PID" 2>/dev/null || { log "QEMU exited at install monitor $i"; return 1; }
  done
  log "install monitor done — inspect oc-05-*.png for completion + Setup Assistant (display kept awake)"
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
  oras pull "$GHCR_REPO:$1" -o .
  [[ -f "$QCOW2_NAME" ]] || { log "FATAL: image $QCOW2_NAME not pulled from :$1"; exit 1; }
  log "image present: $(du -h "$QCOW2_NAME" | cut -f1)"
  [[ -f OVMF_VARS.fd ]] || cp OVMF_VARS-1920x1080.fd OVMF_VARS.fd
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
  log "provision done; capturing turnkey image -> $GHCR_REPO:$GHCR_TAG"
  capture_and_push "$GHCR_TAG"   # tahoe:26 = SA-skipped, user 'cocoon', SSH on first boot
}

picker_size() { stat -f%z "$ARTIFACT_DIR/$1.png" 2>/dev/null || stat -c%s "$ARTIFACT_DIR/$1.png" 2>/dev/null || echo 0; }

boot_macintosh() {  # leave the OpenCore picker into the installed macOS: prefer auto-boot, else sendkey (retry ret)
  sleep 50
  screendump "vf-00"
  if [ "$(picker_size vf-00)" -gt 30000 ]; then
    log "still at picker ($(picker_size vf-00) bytes); selecting Macintosh via sendkey (right once, then ret)"
    mon "sendkey right"; sleep 2
    local a
    for a in $(seq 1 8); do
      mon "sendkey ret"; sleep 12
      screendump "vf-pk-$a"
      if [ "$(picker_size vf-pk-$a)" -lt 30000 ]; then log "left picker after $a ret"; break; fi
    done
  else
    log "OpenCore auto-booted (frame $(picker_size vf-00) bytes)"
  fi
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

stage_slim() {  # SA-INDEPENDENT slim: boot, reclaim stale clusters over SSH, re-push the same tag.
  # Works on SSH-ready images (:26) regardless of the GUI being stuck at Setup Assistant — slimming
  # only needs SSH (sudo+dd work at the SA stage) + the MacHDD discard=unmap,detect-zeroes=unmap.
  boot_macintosh
  log "waiting for SSH (slim is SA-independent; SSH comes up even with the GUI at Setup Assistant)"
  wait_ssh 36 || { log "FATAL: SSH never came up"; return 1; }
  slim_disk
  log "clean shutdown"
  gssh 'echo cocoon | sudo -S shutdown -h now' >/dev/null 2>&1 || true
  sleep 20
  capture_and_push "$GHCR_TAG"   # convert -c drops the now-zeroed/unmapped stale clusters
}

main() {
  require_kvm
  setup_osx_kvm
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
