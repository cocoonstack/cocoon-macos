#!/bin/bash
# Run from a macOS Recovery Terminal against the INSTALLED macOS Data volume.
# Makes the image turnkey + headless-usable without the Setup Assistant GUI:
#   - skip Setup Assistant (.AppleSetupDone)
#   - create a local admin user offline (dscl -f on the volume's dslocal)
#   - drop a first-boot LaunchDaemon that enables Remote Login (SSH), then self-removes
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
echo "=== PROVISION DONE (user=$USER_NAME, SSH on first boot) ==="
