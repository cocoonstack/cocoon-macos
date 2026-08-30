#!/bin/bash
# Run from a macOS Recovery Terminal against the INSTALLED macOS Data volume.
# Makes the image turnkey + headless-usable without the Setup Assistant GUI:
#   - skip Setup Assistant (.AppleSetupDone)
#   - create a local admin user offline (dscl -f on the volume's dslocal)
#   - drop a first-boot LaunchDaemon that enables Remote Login (SSH), then self-removes
#   - install a persistent LaunchDaemon that assigns a per-VM hostname
# Diagnostics are printed so a VNC screenshot shows progress/errors.
set -x
USER_NAME="${USER_NAME:-cocoon}"
USER_PASS="${USER_PASS:-cocoon}"

# Locate the installed Data volume (the writable one holding dslocal).
VOL=""
for v in "/Volumes/Macintosh - Data" "/Volumes/Macintosh" /Volumes/*; do
  if [ -d "$v/private/var/db/dslocal/nodes/Default" ]; then VOL="$v"; break; fi
done
echo "=== DATA VOLUME = [$VOL] ==="
diskutil list | tail -20
[ -n "$VOL" ] || { echo "!!! NO INSTALLED DATA VOLUME FOUND"; exit 1; }

DS="$VOL/private/var/db/dslocal/nodes/Default"

# 1) skip Setup Assistant (cover both path shapes; verify)
for d in "$VOL/private/var/db" "$VOL/var/db"; do
  [ -d "$d" ] && touch "$d/.AppleSetupDone"
done
ls -la "$VOL/private/var/db/.AppleSetupDone" 2>/dev/null && echo "OK .AppleSetupDone" || echo "WARN .AppleSetupDone missing"

# 2) create the admin user directly in the volume's dslocal
U="/Local/Default/Users/$USER_NAME"
dscl -f "$DS" localhost -create "$U"
dscl -f "$DS" localhost -create "$U" UserShell /bin/zsh
dscl -f "$DS" localhost -create "$U" RealName "$USER_NAME"
dscl -f "$DS" localhost -create "$U" UniqueID 501
dscl -f "$DS" localhost -create "$U" PrimaryGroupID 20
dscl -f "$DS" localhost -create "$U" NFSHomeDirectory "/Users/$USER_NAME"
dscl -f "$DS" localhost -passwd "$U" "$USER_PASS"
dscl -f "$DS" localhost -append "/Local/Default/Groups/admin" GroupMembership "$USER_NAME"
# Populate a COMPLETE home from the User Template. macOS 14+ only skips the system Setup
# Assistant (the _mbsetupuser pre-login wizard) when a *complete* local user exists — an empty
# home dir is treated as incomplete, so the wizard still runs. (.AppleSetupDone alone no longer
# suffices on Sonoma+/Tahoe.) .skipbuddy suppresses the per-user buddy.
SYS="${VOL% - Data}"   # the installed System volume is the Data volume's sibling
[ -d "$SYS/System/Library/User Template" ] || for v in /Volumes/*; do
  [ "$v" = "$VOL" ] && continue
  [ -d "$v/System/Library/User Template" ] && SYS="$v" && break
done
echo "=== SYSTEM VOLUME = [$SYS] ==="
mkdir -p "$VOL/Users/$USER_NAME"
if [ -n "$SYS" ]; then
  TPL="$SYS/System/Library/User Template"
  cp -R "$TPL/Non_localized/." "$VOL/Users/$USER_NAME/" 2>/dev/null
  cp -R "$TPL/English.lproj/." "$VOL/Users/$USER_NAME/" 2>/dev/null
  echo "populated home from $TPL"
else
  echo "WARN: User Template not found; home left empty (system SA may still run)"
fi
touch "$VOL/Users/$USER_NAME/.skipbuddy"
chown -R 501:20 "$VOL/Users/$USER_NAME"
echo "OK user $USER_NAME created (complete home from template)"

# 2b) Pre-seed the per-user Setup Assistant DidSee*/LastSeen* keys OFFLINE, into the user's home.
#     This is the load-bearing post-user skip: it pre-answers the login-stage buddy panes
#     (Migration/Transfer-Data, Apple ID, Appearance) that WHITE-SCREEN under QEMU's unaccelerated
#     framebuffer. The repo validated this recipe POST-SA over SSH; the deadlock was that SA never
#     cleared (white Migration pane) so the SSH step never ran — so we write it OFFLINE, before
#     first boot, so SA's pane carousel is bypassed entirely and boot goes straight to the desktop.
PV=""; BV=""
if [ -f "$SYS/System/Library/CoreServices/SystemVersion.plist" ]; then
  PV=$(/usr/libexec/PlistBuddy -c 'Print :ProductVersion' "$SYS/System/Library/CoreServices/SystemVersion.plist" 2>/dev/null)
  BV=$(/usr/libexec/PlistBuddy -c 'Print :ProductBuildVersion' "$SYS/System/Library/CoreServices/SystemVersion.plist" 2>/dev/null)
fi
PV="${PV:-26.5}"; BV="${BV:-25F71}"
echo "=== SA-skip seed: PV=$PV BV=$BV ==="
SAPREF="$VOL/Users/$USER_NAME/Library/Preferences"
mkdir -p "$SAPREF"
cat > "$SAPREF/com.apple.SetupAssistant.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>DidSeeCloudSetup</key><true/>
  <key>DidSeeSiriSetup</key><true/>
  <key>DidSeePrivacy</key><true/>
  <key>DidSeeScreenTime</key><true/>
  <key>DidSeeAppearanceSetup</key><true/>
  <key>DidSeeTouchIDSetup</key><true/>
  <key>DidSeeApplePaySetup</key><true/>
  <key>DidSeeActivationLock</key><true/>
  <key>DidSeeAccessibility</key><true/>
  <key>DidSeeAvatarSetup</key><true/>
  <key>DidSeeSyncSetup</key><true/>
  <key>DidSeeSyncSetup2</key><true/>
  <key>DidSeeTrueTone</key><true/>
  <key>DidSeeTermsOfAddress</key><true/>
  <key>DidSeeLockdownMode</key><true/>
  <key>DidSeeAppStore</key><true/>
  <key>DidSeeiCloudLoginForStorageServices</key><true/>
  <key>GestureMovieSeen</key><string>none</string>
  <key>MiniBuddyShouldLaunchToResumeSetup</key><false/>
  <key>MiniBuddyLaunchReason</key><integer>0</integer>
  <key>LastSeenCloudProductVersion</key><string>$PV</string>
  <key>LastSeenBuddyBuildVersion</key><string>$BV</string>
  <key>LastSeenDiagnosticsProductVersion</key><string>$PV</string>
  <key>LastSeenSiriProductVersion</key><string>$PV</string>
  <key>LastSeenSyncProductVersion</key><string>$PV</string>
</dict></plist>
PLIST
chown -R 501:20 "$VOL/Users/$USER_NAME/Library"
echo "OK seeded com.apple.SetupAssistant.plist"

# 2c) Auto-login OFFLINE: autoLoginUser + /etc/kcpassword -> reaches the DESKTOP (not just the login
#     window). kcpassword = "cocoon" XOR Apple's key, one 12-byte block (bash 3.2 has no printf \xHH,
#     so emit raw bytes via perl pack). FileVault is never enabled here, so auto-login is honored.
cat > "$VOL/Library/Preferences/com.apple.loginwindow.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>autoLoginUser</key><string>$USER_NAME</string>
  <key>autoLoginUserUID</key><integer>501</integer>
</dict></plist>
PLIST
chown 0:0 "$VOL/Library/Preferences/com.apple.loginwindow.plist"; chmod 644 "$VOL/Library/Preferences/com.apple.loginwindow.plist"
mkdir -p "$VOL/etc"
perl -e 'print pack(q{C*},30,230,49,76,189,210,221,234,163,185,31,125)' > "$VOL/etc/kcpassword"
chown 0:0 "$VOL/etc/kcpassword"; chmod 600 "$VOL/etc/kcpassword"
echo "OK kcpassword ($(stat -f %z "$VOL/etc/kcpassword" 2>/dev/null)b) + autoLoginUser=$USER_NAME"
# Suppress the Keyboard Setup Assistant for the QEMU USB keyboard (idVendor 1575 / idProduct 1 -> ANSI 40)
cat > "$VOL/Library/Preferences/com.apple.keyboardtype.plist" <<'PLIST'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict><key>keyboardtype</key><dict><key>1-1575-0</key><integer>40</integer></dict></dict></plist>
PLIST
chown 0:0 "$VOL/Library/Preferences/com.apple.keyboardtype.plist"; chmod 644 "$VOL/Library/Preferences/com.apple.keyboardtype.plist"

# 2d) Belt-and-suspenders ONLY (non-DEP Macs may ignore it): System-scope managed-prefs with
#     SkipSetupItems. "Restore" is the real key for the Migration/Transfer-Data pane (no "Migration"
#     key exists). The pre-created-user (step 2) + DidSee* (2b) + auto-login (2c) is the actual contract.
mkdir -p "$VOL/Library/Managed Preferences"
cat > "$VOL/Library/Managed Preferences/com.apple.SetupAssistant.managed.plist" <<'PLIST'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>SkipSetupItems</key>
  <array>
    <string>Restore</string><string>Welcome</string><string>OSShowcase</string>
    <string>Privacy</string><string>AppleID</string><string>Siri</string>
    <string>Intelligence</string><string>Diagnostics</string><string>Payment</string>
    <string>ScreenTime</string><string>Biometric</string><string>Appearance</string>
    <string>Wallpaper</string><string>TermsOfAddress</string><string>iCloudStorage</string>
    <string>FileVault</string><string>Accessibility</string><string>SoftwareUpdate</string>
  </array>
</dict></plist>
PLIST
chown 0:0 "$VOL/Library/Managed Preferences/com.apple.SetupAssistant.managed.plist"
chmod 644 "$VOL/Library/Managed Preferences/com.apple.SetupAssistant.managed.plist"
echo "OK SkipSetupItems managed-pref dropped (belt-and-suspenders)"

# 3) first-boot daemon: enable Remote Login (SSH) + disable display sleep, then remove itself.
mkdir -p "$VOL/Library/LaunchDaemons" "$VOL/usr/local/bin"
cat > "$VOL/usr/local/bin/cocoon-firstboot.sh" <<'SH'
#!/bin/bash
exec >>/var/log/cocoon-firstboot.log 2>&1
echo "=== cocoon-firstboot $(date) ==="
/usr/sbin/systemsetup -f -setremotelogin on
/bin/launchctl enable system/com.openssh.sshd
/bin/launchctl bootstrap system /System/Library/LaunchDaemons/ssh.plist
/bin/launchctl kickstart -k system/com.openssh.sshd
echo "remotelogin: $(/usr/sbin/systemsetup -getremotelogin)"
# Never sleep the display: the QEMU/VNC framebuffer is only repainted while macOS keeps the display
# awake — once it sleeps, VNC shows a blank (white/black) screen even though the guest is fine. This
# is system-wide + persistent (pmset prefs), so it covers the pre-login loginwindow too.
/usr/bin/pmset -a displaysleep 0 sleep 0 disablesleep 1
echo "pmset: $(/usr/bin/pmset -g | tr '\n' ' ')"
# Perf: kill the post-first-boot background storm. A fresh image spends its first many minutes with
# Spotlight (mds/mdworker) indexing the whole disk plus a few analysis agents churning CPU+disk —
# that is the "sluggish right after boot" feeling. Headless/demo VMs need none of it. mds may not be
# up yet at RunAtLoad, so retry a few times (SSH is already enabled above, so this never delays login).
for i in 1 2 3 4 5; do /usr/bin/mdutil -a -i off >/dev/null 2>&1 && break; sleep 5; done
/usr/bin/mdutil -a -i off || true   # disable Spotlight indexing on every volume
/usr/bin/mdutil -a -d   || true     # and turn the indexer off entirely
for svc in com.apple.photoanalysisd com.apple.mediaanalysisd com.apple.parsecd com.apple.suggestd; do
  /bin/launchctl disable "user/501/$svc" 2>/dev/null || true
done
# XProtect malware scans (startup + periodic) burn ~75% CPU for ~2min on every boot (measured on a
# 2-vCPU tahoe:26 VM); a disposable headless VM needs no on-access scanning, so disable the scan
# launchds. system domain, so the persisted disable-override survives this daemon self-removing.
for svc in com.apple.XProtect.daemon.scan.startup com.apple.XProtect.daemon.scan; do
  /bin/launchctl disable "system/$svc" 2>/dev/null || true
done
echo "spotlight: $(/usr/bin/mdutil -a -s 2>/dev/null | tr '\n' ' ')"
/bin/rm -f /Library/LaunchDaemons/com.cocoon.firstboot.plist /usr/local/bin/cocoon-firstboot.sh
SH
chmod 755 "$VOL/usr/local/bin/cocoon-firstboot.sh"
cat > "$VOL/Library/LaunchDaemons/com.cocoon.firstboot.plist" <<'PLIST'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>com.cocoon.firstboot</string>
  <key>ProgramArguments</key><array><string>/bin/bash</string><string>/usr/local/bin/cocoon-firstboot.sh</string></array>
  <key>RunAtLoad</key><true/>
  <key>StandardErrorPath</key><string>/var/log/cocoon-firstboot.err</string>
</dict></plist>
PLIST
# LaunchDaemons must be owned root:wheel and not group/other-writable, else launchd ignores them
chown 0:0 "$VOL/Library/LaunchDaemons/com.cocoon.firstboot.plist" "$VOL/usr/local/bin/cocoon-firstboot.sh"
chmod 644 "$VOL/Library/LaunchDaemons/com.cocoon.firstboot.plist"
ls -la "$VOL/Library/LaunchDaemons/com.cocoon.firstboot.plist"

# 4) Persistent per-VM hostname. With --random-smbios, derive all three names
# from the unique SMBIOS UUID; without it clones still share the golden UUID.
mkdir -p "$VOL/usr/local/sbin"
cat > "$VOL/usr/local/sbin/cocoon-set-hostname" <<'SH'
#!/bin/zsh

set -eu

log=/var/log/cocoon-set-hostname.log
exec >>"$log" 2>&1

uuid=$(/usr/sbin/ioreg -rd1 -c IOPlatformExpertDevice | /usr/bin/awk -F '"' '/IOPlatformUUID/ {print $(NF-1); exit}')
suffix=$(printf '%s' "$uuid" | /usr/bin/tr '[:upper:]' '[:lower:]' | /usr/bin/tr -cd 'a-f0-9' | /usr/bin/cut -c1-8)
if [[ ${#suffix} -ne 8 ]]; then
  echo "$(date -u +%Y-%m-%dT%H:%M:%SZ) unable to derive hostname from IOPlatformUUID=$uuid"
  exit 1
fi

name="cocoon-mac-$suffix"
for key in ComputerName LocalHostName HostName; do
  current=$(/usr/sbin/scutil --get "$key" 2>/dev/null || true)
  if [[ "$current" != "$name" ]]; then
    /usr/sbin/scutil --set "$key" "$name"
  fi
done
echo "$(date -u +%Y-%m-%dT%H:%M:%SZ) hostname=$name uuid=$uuid"
SH
chmod 755 "$VOL/usr/local/sbin/cocoon-set-hostname"
cat > "$VOL/Library/LaunchDaemons/com.cocoon.set-hostname.plist" <<'PLIST'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>com.cocoon.set-hostname</string>
  <key>ProgramArguments</key><array><string>/usr/local/sbin/cocoon-set-hostname</string></array>
  <key>RunAtLoad</key><true/>
  <key>StandardOutPath</key><string>/var/log/cocoon-set-hostname.launchd.log</string>
  <key>StandardErrorPath</key><string>/var/log/cocoon-set-hostname.launchd.err</string>
</dict></plist>
PLIST
chown 0:0 "$VOL/Library/LaunchDaemons/com.cocoon.set-hostname.plist" "$VOL/usr/local/sbin/cocoon-set-hostname"
chmod 644 "$VOL/Library/LaunchDaemons/com.cocoon.set-hostname.plist"
ls -la "$VOL/Library/LaunchDaemons/com.cocoon.set-hostname.plist"
echo "=== PROVISION DONE (user=$USER_NAME, SSH on first boot) ==="
