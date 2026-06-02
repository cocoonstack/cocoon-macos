#!/usr/bin/env bash
# cm-smoke.sh — cocoon-macos E2E regression, modeled on cocoonv2's issue28-repro.sh.
#
# Two tiers:
#   [DUMMY]  file-level lifecycle on a tiny throwaway qcow2 — NO macOS boot. Fast, runs on any
#            x86 Linux/KVM host (and the --net auto-TAP rows need root + a test bridge). CI-able.
#   [REAL]   boots ghcr tahoe:26, passes the OpenCore picker over the HMP monitor, asserts SSH.
#            Gated behind --real (needs /dev/kvm + the ~15GB image + OVMF/OpenCore loaders).
#
# Idempotent: cleans state-dir + test bridge + leftover bt*/netns at start AND end.
#
# Usage:
#   sudo ./cm-smoke.sh                 # [DUMMY] tier only
#   sudo ./cm-smoke.sh --real          # [DUMMY] then [REAL] boot of tahoe:26
#   sudo ./cm-smoke.sh --no-net        # skip the root/bridge --net auto-create rows
#   sudo ./cm-smoke.sh cleanup         # tear down and exit
#
# Env overrides:
#   CM_BIN        path to the cocoon-macos binary           (default: ./cocoon-macos)
#   CM_HOME       state-dir                                 (default: /tmp/cm-smoke-home)
#   CM_BRIDGE     test bridge for --net tap|bridge          (default: cmsmoke0)
#   CM_TAHOE_REF  ghcr image ref for [REAL]                 (default: ghcr.io/cocoonstack/cocoon-macos/tahoe:26)
#   CM_OVMF_CODE  OVMF_CODE_4M.fd                            ([REAL]; required)
#   CM_OVMF_VARS  OVMF_VARS.fd template                     ([REAL]; required)
#   CM_OPENCORE   OpenCore.qcow2                            ([REAL]; required)
#   CM_SSH_PORT   host port forwarded to guest :22 ([REAL]) (default: 2299)

set -uo pipefail

# ---------------------------------------------------------------------------- config
CM_BIN=${CM_BIN:-./cocoon-macos}
CM_HOME=${CM_HOME:-/tmp/cm-smoke-home}
CM_BRIDGE=${CM_BRIDGE:-cmsmoke0}
CM_TAHOE_REF=${CM_TAHOE_REF:-ghcr.io/cocoonstack/cocoon-macos/tahoe:26}
CM_SSH_PORT=${CM_SSH_PORT:-2299}
DUMMY_BASE=$CM_HOME/_dummy-base.qcow2     # tiny throwaway "golden" image
DUMMY_VARS=$CM_HOME/_dummy-vars.fd        # stand-in OVMF_VARS template (raw .fd)
# OpenCore: a REAL one (CM_OPENCORE) lets --random-smbios actually qemu-nbd-inject PlatformInfo; a
# fake empty qcow2 has no EFI partition/config.plist to mount, so the SMBIOS + clone-identity rows
# SKIP unless a real OpenCore is supplied. Non-injection rows work fine with the fake.
OC_REAL=0
if [ -n "${CM_OPENCORE:-}" ] && [ -f "${CM_OPENCORE}" ]; then DUMMY_OC=$CM_OPENCORE; OC_REAL=1; else DUMMY_OC=$CM_HOME/_dummy-oc.qcow2; fi
# faithful qemu stand-in for proc-lifecycle: argv0 basename == qemu-system-x86_64 + the disk path in
# argv, so terminate()'s PID-reuse-safe VerifyProcessCmdline match actually reaps it (a bare sleeper
# would NOT match — that's the whole point of the cmdline check).
QEMU_STUB=/tmp/cm-qemu-stub/qemu-system-x86_64

PASS=0; FAIL=0; SKIP=0
declare -a RESULTS

DO_REAL=0; DO_DUMMY=1; DO_NET=1
for a in "$@"; do
  case "$a" in
    --real)      DO_REAL=1 ;;
    --real-only) DO_REAL=1; DO_DUMMY=0 ;;   # REAL boot only — reuse a store that already has the image
    --no-net)    DO_NET=0 ;;
    cleanup)     CLEAN_ONLY=1 ;;
  esac
done

log()  { echo "[$(date +%H:%M:%S)] $*"; }
cm()   { "$CM_BIN" --state-dir "$CM_HOME" "$@"; }
# every vm/image subcommand takes --state-dir as a persistent flag; inject it after the group word
vm()   { "$CM_BIN" vm "$1" --state-dir "$CM_HOME" "${@:2}"; }
img()  { "$CM_BIN" image "$1" --state-dir "$CM_HOME" "${@:2}"; }

pass() { PASS=$((PASS+1)); RESULTS+=("PASS  $1"); log "PASS  $1"; }
fail() { FAIL=$((FAIL+1)); RESULTS+=("FAIL  $1  -- $2"); log "FAIL  $1  -- $2"; }
skip() { SKIP=$((SKIP+1)); RESULTS+=("SKIP  $1  -- $2"); log "SKIP  $1  -- $2"; }

# check NAME CMD...: run CMD; PASS on rc0 else FAIL with the captured output line
check() { local name=$1; shift; local out; if out=$("$@" 2>&1); then pass "$name"; else fail "$name" "${out:-rc=$?}"; fi; }

jqv() { python3 -c 'import json,sys; d=json.load(sys.stdin); print(eval(sys.argv[1]))' "$1"; }

# ---------------------------------------------------------------------------- inspect helpers
rec_field() { vm inspect "$1" 2>/dev/null | jqv "d.get('$2','')"; }

# ---------------------------------------------------------------------------- cleanup / idempotency
clean_state() {
  log "==== CLEAN ===="
  # 1) terminate + rm every VM in the store (rm tears down owned TAP/netns)
  if [ -d "$CM_HOME/vms" ]; then
    for d in "$CM_HOME"/vms/*/; do
      [ -d "$d" ] || continue
      local n; n=$(basename "$d")
      vm rm "$n" >/dev/null 2>&1 || true
    done
  fi
  # 2) belt-and-suspenders: kill any qemu stand-ins we spawned
  pkill -f "$QEMU_STUB" 2>/dev/null || true
  # 3) nuke leftover cocoon bridge TAPs (bt<8hex>-N) + any test netns, in case rm raced/failed
  if command -v ip >/dev/null 2>&1; then
    ip -o link show 2>/dev/null | grep -oE 'bt[0-9a-f]{1,8}-[0-9]+' | sort -u | while read -r t; do
      ip link del "$t" 2>/dev/null || true
    done
    ip -o netns list 2>/dev/null | awk '{print $1}' | grep -E '^cni-|cocoon|cmsmoke' | while read -r ns; do
      ip netns del "$ns" 2>/dev/null || true
    done
    ip link show "$CM_BRIDGE" >/dev/null 2>&1 && { ip link set "$CM_BRIDGE" down 2>/dev/null; ip link del "$CM_BRIDGE" 2>/dev/null; }
  fi
  # preserve the pulled image store (cloudimg/) so re-runs / the REAL tier never re-download ~15GB
  rm -rf "$CM_HOME/vms" "$(dirname "$QEMU_STUB")"
  rm -f "$DUMMY_BASE" "$DUMMY_VARS"
  [ "$OC_REAL" = 1 ] || rm -f "$DUMMY_OC"   # never delete a real user-supplied OpenCore
}

setup_fixtures() {
  mkdir -p "$CM_HOME"
  # tiny throwaway base; never booted in DUMMY tier, only used for overlay/backing-chain assertions
  qemu-img create -f qcow2 "$DUMMY_BASE" 64M >/dev/null
  [ "$OC_REAL" = 1 ] || qemu-img create -f qcow2 "$DUMMY_OC" 16M >/dev/null
  : > "$DUMMY_VARS"   # raw .fd stand-in (imagesToSnapshot excludes raw NVRAM — that's the point of a row)
  # faithful qemu stand-in (a renamed long-runner): argv0 basename == qemu-system-x86_64 and we pass
  # the disk path as an arg, so terminate()'s cmdline match reaps it. `tail -f <disk>` runs forever.
  mkdir -p "$(dirname "$QEMU_STUB")"
  cp "$(command -v tail)" "$QEMU_STUB"
  # test bridge for --net tap|bridge rows
  if [ "$DO_NET" = 1 ] && command -v ip >/dev/null 2>&1; then
    ip link add "$CM_BRIDGE" type bridge 2>/dev/null || true
    ip link set "$CM_BRIDGE" up 2>/dev/null || true
  fi
}

# ---------------------------------------------------------------------------- assertion primitives
overlay_backing() { qemu-img info --output=json "$1" 2>/dev/null | jqv "d.get('full-backing-filename', d.get('backing-filename',''))"; }
qcow2_has_snap()  { qemu-img snapshot -l "$1" 2>/dev/null | awk '{print $2}' | grep -qx "$2"; }
proc_alive()      { kill -0 "$1" 2>/dev/null; }
no_proc_for_dir() { ! pgrep -fa "qemu-system-x86_64" 2>/dev/null | grep -q "$1"; }

# =================================================================================================
# [DUMMY] TIER
# =================================================================================================
run_dummy() {
  log "############ [DUMMY] TIER ############"

  # --- image store -------------------------------------------------------------------------------
  out=$(img list 2>&1); if [ "$(echo "$out" | tr -d '[:space:]')" = "[]" ]; then
    pass "[DUMMY] image list empty-store => []"
  else fail "[DUMMY] image list empty-store" "got: $out"; fi
  check "[DUMMY] image rm absent-ref is a no-op (no crash)" img rm does-not-exist:tag

  # --- vm create: CoW overlay on shared base + per-VM OVMF_VARS copy + vm.json -------------------
  if vm create "$DUMMY_BASE" -n d1 --net user \
        --opencore "$DUMMY_OC" --ovmf-code "$DUMMY_VARS" --ovmf-vars "$DUMMY_VARS" >/dev/null 2>&1; then
    d1_disk=$(rec_field d1 disk)
    if [ -f "$d1_disk" ]; then pass "[DUMMY] vm create bakes overlay disk.qcow2"; else fail "[DUMMY] vm create overlay" "no disk at $d1_disk"; fi
    bk=$(overlay_backing "$d1_disk")
    if [ "$bk" = "$DUMMY_BASE" ]; then pass "[DUMMY] overlay backing == shared base"; else fail "[DUMMY] overlay backing" "got $bk want $DUMMY_BASE"; fi
    if [ -f "$CM_HOME/vms/d1/OVMF_VARS.fd" ]; then pass "[DUMMY] OVMF_VARS copied per-VM"; else fail "[DUMMY] OVMF_VARS copy" "missing"; fi
    if [ -f "$CM_HOME/vms/d1/vm.json" ]; then pass "[DUMMY] vm.json written"; else fail "[DUMMY] vm.json" "missing"; fi
  else fail "[DUMMY] vm create" "create returned nonzero"; fi

  # --- negative path: missing --opencore is rejected ---------------------------------------------
  if vm create "$DUMMY_BASE" -n dneg --ovmf-code "$DUMMY_VARS" --ovmf-vars "$DUMMY_VARS" >/dev/null 2>&1; then
    fail "[DUMMY] create without --opencore rejected" "create unexpectedly succeeded"
  else pass "[DUMMY] create without --opencore rejected"; fi

  # --- vm list / inspect -------------------------------------------------------------------------
  cnt=$(vm list 2>/dev/null | jqv "len([r for r in d if r['name']=='d1'])")
  if [ "$cnt" = "1" ]; then pass "[DUMMY] vm list shows d1"; else fail "[DUMMY] vm list" "count=$cnt"; fi
  st=$(rec_field d1 name)
  if [ "$st" = "d1" ]; then pass "[DUMMY] vm inspect returns record JSON"; else fail "[DUMMY] vm inspect" "name=$st"; fi

  # --- vm console is informational-only ----------------------------------------------------------
  out=$(vm console d1 2>&1)
  if echo "$out" | grep -q "SSH:"; then pass "[DUMMY] vm console prints hint, does not block/attach"; else fail "[DUMMY] vm console" "no hint: $out"; fi

  # --- --random-smbios identity uniqueness (needs root + nbd; else SKIP) -------------------------
  if [ "$OC_REAL" = 1 ] && [ "$(id -u)" = 0 ] && modprobe nbd max_part=8 2>/dev/null && [ -e /dev/nbd0 ]; then
    vm create "$DUMMY_BASE" -n s1 --net user --random-smbios --opencore "$DUMMY_OC" --ovmf-code "$DUMMY_VARS" --ovmf-vars "$DUMMY_VARS" >/dev/null 2>&1
    vm create "$DUMMY_BASE" -n s2 --net user --random-smbios --opencore "$DUMMY_OC" --ovmf-code "$DUMMY_VARS" --ovmf-vars "$DUMMY_VARS" >/dev/null 2>&1
    s1_ser=$(vm inspect s1 2>/dev/null | jqv "d.get('smbios',{}).get('serial','')")
    s2_ser=$(vm inspect s2 2>/dev/null | jqv "d.get('smbios',{}).get('serial','')")
    s1_mac=$(rec_field s1 mac); s2_mac=$(rec_field s2 mac)
    if [ -n "$s1_ser" ] && [ "$s1_ser" != "$s2_ser" ]; then pass "[DUMMY] two --random-smbios get distinct serials"; else fail "[DUMMY] smbios serial uniqueness" "s1=$s1_ser s2=$s2_ser"; fi
    if [ -n "$s1_mac" ] && [ "$s1_mac" != "$s2_mac" ]; then pass "[DUMMY] two --random-smbios get distinct MACs"; else fail "[DUMMY] smbios MAC uniqueness" "s1=$s1_mac s2=$s2_mac"; fi
    s1_rom=$(vm inspect s1 2>/dev/null | jqv "d.get('smbios',{}).get('rom','')")
    romfmt=$(echo "$s1_rom" | sed -E 's/(..)(..)(..)(..)(..)(..)/\1:\2:\3:\4:\5:\6/')
    if [ "$romfmt" = "$s1_mac" ]; then pass "[DUMMY] guest MAC == SMBIOS ROM"; else fail "[DUMMY] MAC==ROM" "rom=$romfmt mac=$s1_mac"; fi
  else
    skip "[DUMMY] --random-smbios uniqueness" "needs root + nbd + a real --opencore (set CM_OPENCORE) for SMBIOS injection"
  fi

  # --- vm snapshot (stopped) records tag ; restore reverts ; no-snapshot errors ------------------
  vm snapshot d1 --tag clean >/dev/null 2>&1
  if qcow2_has_snap "$d1_disk" clean; then pass "[DUMMY] snapshot tag present in disk.qcow2"; else fail "[DUMMY] snapshot tag" "tag 'clean' not in $d1_disk"; fi
  if vm inspect d1 2>/dev/null | jqv "'clean' in d.get('snapshots',[])" | grep -q True; then pass "[DUMMY] snapshot tag recorded in vm.json"; else fail "[DUMMY] snapshot recorded" "not in record"; fi
  check "[DUMMY] restore default-newest reverts" vm restore d1
  vm create "$DUMMY_BASE" -n dnos --net user --opencore "$DUMMY_OC" --ovmf-code "$DUMMY_VARS" --ovmf-vars "$DUMMY_VARS" >/dev/null 2>&1
  if vm restore dnos >/dev/null 2>&1; then fail "[DUMMY] restore-no-snapshots rejected" "unexpectedly succeeded"; else pass "[DUMMY] restore-no-snapshots rejected"; fi

  # --- vm clone: fresh overlay on SAME base + distinct identity ----------------------------------
  if [ "$OC_REAL" = 1 ] && [ "$(id -u)" = 0 ] && [ -e /dev/nbd0 ]; then SRC=s1; else SRC=d1; fi
  vm clone "$SRC" -n c1 --net user --opencore "$DUMMY_OC" --ovmf-code "$DUMMY_VARS" --ovmf-vars "$DUMMY_VARS" >/dev/null 2>&1
  c1_disk=$(rec_field c1 disk)
  cbk=$(overlay_backing "$c1_disk")
  if [ "$cbk" = "$DUMMY_BASE" ]; then pass "[DUMMY] clone backing == shared base (not src overlay)"; else fail "[DUMMY] clone backing" "got $cbk want $DUMMY_BASE"; fi
  if [ "$SRC" = s1 ]; then
    vm clone s1 -n c2 --net user --opencore "$DUMMY_OC" --ovmf-code "$DUMMY_VARS" --ovmf-vars "$DUMMY_VARS" >/dev/null 2>&1
    c1m=$(rec_field c1 mac); c2m=$(rec_field c2 mac); sm=$(rec_field s1 mac)
    if [ "$c1m" != "$c2m" ] && [ "$c1m" != "$sm" ]; then pass "[DUMMY] two clones have distinct MAC (vs each other + src)"; else fail "[DUMMY] clone MAC uniqueness" "src=$sm c1=$c1m c2=$c2m"; fi
    c1s=$(vm inspect c1 2>/dev/null | jqv "d.get('smbios',{}).get('serial','')")
    c2s=$(vm inspect c2 2>/dev/null | jqv "d.get('smbios',{}).get('serial','')")
    if [ -n "$c1s" ] && [ "$c1s" != "$c2s" ]; then pass "[DUMMY] two clones have distinct serial"; else fail "[DUMMY] clone serial uniqueness" "c1=$c1s c2=$c2s"; fi
    vm clone c1 -n c3 --net user --opencore "$DUMMY_OC" --ovmf-code "$DUMMY_VARS" --ovmf-vars "$DUMMY_VARS" >/dev/null 2>&1
    c3bk=$(overlay_backing "$(rec_field c3 disk)")
    if [ "$c3bk" = "$DUMMY_BASE" ]; then pass "[DUMMY] clone-of-clone backing == shared base"; else fail "[DUMMY] clone-of-clone backing" "got $c3bk"; fi
  else
    skip "[DUMMY] clone identity uniqueness" "needs root + nbd for --random-smbios src"
  fi

  # --- process lifecycle: stop sets PID=0 ; no leaked proc ---------------------------------------
  pid_d1=$(setsid "$QEMU_STUB" -f "$d1_disk" >/dev/null 2>&1 & echo $!)
  python3 - "$CM_HOME/vms/d1/vm.json" "$pid_d1" <<'PY'
import json,sys
p=sys.argv[1]; pid=int(sys.argv[2])
d=json.load(open(p)); d['pid']=pid; json.dump(d,open(p,'w'))
PY
  if proc_alive "$pid_d1"; then
    vm stop d1 >/dev/null 2>&1
    newpid=$(rec_field d1 pid)
    if [ "$newpid" = "0" ] && ! proc_alive "$pid_d1"; then pass "[DUMMY] stop terminates proc + sets PID=0"; else fail "[DUMMY] stop" "pid=$newpid alive=$(proc_alive "$pid_d1" && echo y || echo n)"; fi
  else skip "[DUMMY] stop lifecycle" "sleeper failed to start"; fi

  # --- --net auto-create TAP + teardown-on-rm (root + bridge) ------------------------------------
  if [ "$DO_NET" = 1 ] && [ "$(id -u)" = 0 ] && command -v ip >/dev/null 2>&1 && ip link show "$CM_BRIDGE" >/dev/null 2>&1; then
    if vm create "$DUMMY_BASE" -n net1 --net bridge --bridge "$CM_BRIDGE" \
          --opencore "$DUMMY_OC" --ovmf-code "$DUMMY_VARS" --ovmf-vars "$DUMMY_VARS" >/dev/null 2>&1; then
      tap=$(rec_field net1 tap)
      if [ -n "$tap" ] && ip link show "$tap" >/dev/null 2>&1; then pass "[DUMMY] --net bridge auto-creates TAP ($tap)"; else fail "[DUMMY] --net bridge TAP create" "tap=$tap not present"; fi
      owned=$(rec_field net1 tap_owned)
      if [ "$owned" = "True" ]; then pass "[DUMMY] auto-created TAP marked tap_owned"; else fail "[DUMMY] tap_owned" "owned=$owned"; fi
      vm rm net1 >/dev/null 2>&1
      if ! ip link show "$tap" >/dev/null 2>&1; then pass "[DUMMY] rm tears down auto-TAP (no leak, no --bridge needed)"; else fail "[DUMMY] TAP leak on rm" "$tap still present"; fi
    else fail "[DUMMY] --net bridge create" "create returned nonzero"; fi

    ip tuntap add dev cmsmoketap0 mode tap 2>/dev/null || true
    ip link set cmsmoketap0 master "$CM_BRIDGE" 2>/dev/null || true
    vm create "$DUMMY_BASE" -n net2 --net tap --tap cmsmoketap0 \
        --opencore "$DUMMY_OC" --ovmf-code "$DUMMY_VARS" --ovmf-vars "$DUMMY_VARS" >/dev/null 2>&1
    owned2=$(rec_field net2 tap_owned)
    vm rm net2 >/dev/null 2>&1
    if [ "$owned2" != "True" ] && ip link show cmsmoketap0 >/dev/null 2>&1; then
      pass "[DUMMY] user --tap not torn down on rm (TapOwned=false)"
    else fail "[DUMMY] user --tap preservation" "owned=$owned2 present=$(ip link show cmsmoketap0 >/dev/null 2>&1 && echo y || echo n)"; fi
    ip link del cmsmoketap0 2>/dev/null || true

    if vm create "$DUMMY_BASE" -n net3 --net bridge \
          --opencore "$DUMMY_OC" --ovmf-code "$DUMMY_VARS" --ovmf-vars "$DUMMY_VARS" >/dev/null 2>&1; then
      fail "[DUMMY] --net bridge without --bridge rejected" "unexpectedly succeeded"
    else pass "[DUMMY] --net bridge without --bridge rejected"; fi
  else
    skip "[DUMMY] --net auto-create/teardown rows" "needs root + ip + test bridge $CM_BRIDGE (or --no-net)"
  fi

  post_conditions_dummy
}

post_conditions_dummy() {
  log "---- [DUMMY] post-conditions ----"
  leaks=$(ip -o link show 2>/dev/null | grep -oE 'bt[0-9a-f]{1,8}-[0-9]+' | sort -u || true)
  if [ -z "$leaks" ]; then pass "[POST] no leaked bt* TAPs"; else fail "[POST] leaked bt* TAPs" "$leaks"; fi
  if ! pgrep -f "$QEMU_STUB" >/dev/null 2>&1; then pass "[POST] no leaked stand-in procs"; else fail "[POST] leaked procs" "$(pgrep -fa "$QEMU_STUB")"; fi
  bad=0
  for d in "$CM_HOME"/vms/*/disk.qcow2; do
    [ -f "$d" ] || continue
    [ "$(overlay_backing "$d")" = "$DUMMY_BASE" ] || { bad=1; log "  bad backing on $d"; }
  done
  if [ "$bad" = 0 ]; then pass "[POST] all overlays back onto the shared immutable base"; else fail "[POST] backing-chain integrity" "an overlay diverged from base"; fi
}

# =================================================================================================
# [REAL] TIER — boots tahoe:26, passes the OpenCore picker over HMP, asserts SSH
# =================================================================================================
# hmp sends one HMP command to a monitor unix socket (mirror of build-qemu-macos.sh `mon`)
hmp() { local sock=$1 cmd=$2; printf '%s\n' "$cmd" | timeout 5 socat - "UNIX-CONNECT:$sock" >/dev/null 2>&1 || true; }
gssh() { sshpass -p cocoon ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=8 -p "$CM_SSH_PORT" cocoon@localhost "$@"; }

run_real() {
  log "############ [REAL] TIER (tahoe:26 boot) ############"
  for v in CM_OVMF_CODE CM_OVMF_VARS CM_OPENCORE; do
    if [ -z "${!v:-}" ] || [ ! -f "${!v}" ]; then skip "[REAL] all boot rows" "$v unset or file missing"; return; fi
  done
  for b in qemu-system-x86_64 socat sshpass oras; do command -v "$b" >/dev/null 2>&1 || { skip "[REAL] all boot rows" "missing $b"; return; }; done
  [ -e /dev/kvm ] && [ -r /dev/kvm ] && [ -w /dev/kvm ] || { skip "[REAL] all boot rows" "/dev/kvm not rw"; return; }

  if img inspect "$CM_TAHOE_REF" >/dev/null 2>&1; then
    dig=$(img inspect "$CM_TAHOE_REF" 2>/dev/null | jqv "d.get('digest', d.get('image_digest',''))")
    pass "[REAL] image already in store, reuse (digest=$dig) — skip ~15GB re-pull"
  else
    log "[REAL] pulling $CM_TAHOE_REF (may be ~15GB)"
    if img pull "$CM_TAHOE_REF" >/dev/null 2>&1; then
      dig=$(img inspect "$CM_TAHOE_REF" 2>/dev/null | jqv "d.get('digest', d.get('image_digest',''))")
      if [ -n "$dig" ]; then pass "[REAL] image pull => content-addressed blob (digest=$dig)"; else fail "[REAL] image pull digest" "no digest after pull"; fi
    else fail "[REAL] image pull" "oras/cloudimg pull failed"; return; fi
  fi

  log "[REAL] vm run from store ref (real OVMF + OpenCore + --random-smbios)"
  if ! vm run "$CM_TAHOE_REF" -n real1 --cpus 4 --memory 8192 --ssh-port "$CM_SSH_PORT" --vnc 9 \
        --random-smbios --opencore "$CM_OPENCORE" --ovmf-code "$CM_OVMF_CODE" --ovmf-vars "$CM_OVMF_VARS" >/dev/null 2>&1; then
    fail "[REAL] vm run launch" "qemu launch returned nonzero"; return
  fi
  pid=$(rec_field real1 pid)
  if proc_alive "$pid"; then pass "[REAL] qemu daemonized + PID alive ($pid)"; else fail "[REAL] qemu PID alive" "pid=$pid dead"; return; fi
  qmp_sock=$CM_HOME/vms/real1/qmp.sock
  mon_sock=$CM_HOME/vms/real1/monitor.sock

  log "[REAL] nudging OpenCore picker over HMP monitor"
  sleep 45
  hmp "$mon_sock" "sendkey right"; sleep 2
  for a in $(seq 1 8); do hmp "$mon_sock" "sendkey ret"; sleep 10; done
  pass "[REAL] OpenCore picker nudged (sendkey right; ret x8)"

  log "[REAL] waiting for SSH on cocoon@localhost:$CM_SSH_PORT"
  ok=""
  for w in $(seq 1 18); do
    if gssh 'sw_vers -productVersion' >/tmp/cm-real-sw.txt 2>&1; then ok=1; break; fi
    python3 "$(dirname "$0")/qmp-input.py" "$qmp_sock" move $((60 + w * 13)) 400 2>/dev/null || true
    sleep 20
  done
  if [ -n "$ok" ]; then
    ver=$(cat /tmp/cm-real-sw.txt)
    if echo "$ver" | grep -q "^26"; then pass "[REAL] SSH-ready, guest is macOS $ver"; else fail "[REAL] SSH version" "got '$ver' (want 26.x)"; fi
    inj=$(vm inspect real1 2>/dev/null | jqv "d.get('smbios',{}).get('serial','')")
    guest=$(gssh "system_profiler SPHardwareDataType 2>/dev/null | awk '/Serial Number/{print \$NF}'" 2>/dev/null)
    if [ -n "$inj" ] && [ "$inj" = "$guest" ]; then pass "[REAL] in-guest serial matches injected SMBIOS ($inj)"; else fail "[REAL] in-guest SMBIOS" "injected=$inj guest=$guest"; fi
  else
    fail "[REAL] SSH-ready" "no SSH after ~6min; inspect VNC 127.0.0.1:5909 + $(cat /tmp/cm-real-sw.txt 2>/dev/null)"
  fi

  vm stop real1 >/dev/null 2>&1
  sleep 2
  if no_proc_for_dir "vms/real1"; then pass "[REAL] stop leaves no orphan qemu for real1"; else fail "[REAL] orphan qemu after stop" "$(pgrep -fa qemu-system-x86_64 | grep real1)"; fi
}

# =================================================================================================
# MAIN
# =================================================================================================
if [ "${CLEAN_ONLY:-0}" = 1 ]; then clean_state; exit 0; fi

trap 'clean_state' EXIT
clean_state           # idempotent start
setup_fixtures

[ "$DO_DUMMY" = 1 ] && run_dummy || skip "[DUMMY] tier" "--real-only requested"
[ "$DO_REAL" = 1 ] && run_real || skip "[REAL] tier" "pass --real (+ CM_OVMF_*/CM_OPENCORE) to enable"

# ---------------------------------------------------------------------------- tally
echo
echo "================= cm-smoke RESULTS ================="
for r in "${RESULTS[@]}"; do echo "  $r"; done
echo "----------------------------------------------------"
echo "  PASS=$PASS  FAIL=$FAIL  SKIP=$SKIP"
echo "===================================================="
[ "$FAIL" = 0 ]