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

# 1) skip Setup Assistant
touch "$VOL/private/var/db/.AppleSetupDone" && echo "OK .AppleSetupDone"

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
mkdir -p "$VOL/Users/$USER_NAME" && echo "OK user $USER_NAME created"

# 3) first-boot daemon: enable Remote Login (SSH), then remove itself
mkdir -p "$VOL/Library/LaunchDaemons" "$VOL/usr/local/bin"
cat > "$VOL/usr/local/bin/cocoon-firstboot.sh" <<'SH'
#!/bin/bash
/usr/sbin/systemsetup -setremotelogin on
/bin/rm -f /Library/LaunchDaemons/com.cocoon.firstboot.plist /usr/local/bin/cocoon-firstboot.sh
SH
chmod +x "$VOL/usr/local/bin/cocoon-firstboot.sh"
cat > "$VOL/Library/LaunchDaemons/com.cocoon.firstboot.plist" <<'PLIST'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>com.cocoon.firstboot</string>
  <key>ProgramArguments</key><array><string>/usr/local/bin/cocoon-firstboot.sh</string></array>
  <key>RunAtLoad</key><true/>
</dict></plist>
PLIST
echo "=== PROVISION DONE (user=$USER_NAME, SSH on first boot) ==="
