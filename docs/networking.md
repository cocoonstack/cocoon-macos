# Networking & VNC

## Network modes (`--net`)

| Mode | What it does |
|------|--------------|
| `user` (default) | User-mode SLIRP. Combine with `--ssh-port N` to forward `localhost:N → guest:22`. Works everywhere. |
| `tap` | Attach to a pre-created host TAP verbatim (`--tap tap0`) — the bridge/plane owns IP + forwarding. |
| `bridge` | Auto-create a TAP on an existing Linux bridge (`--bridge br0`) via cocoon's `network/bridge`. |
| `cni` | Auto-create a TAP inside a per-VM netns via cocoon's `network/cni` (TC-redirect plane). |

`tap`/`bridge`/`cni` make a macOS VM join the **same** forwarding plane as cocoon's Cloud
Hypervisor / Firecracker VMs on the node, so the guest can DHCP a **real LAN IP** from the upstream
network. In `tap` and `bridge` mode the guest NIC MAC stays equal to the SMBIOS ROM. Auto-create
(`bridge`/`cni`) is Linux-only (needs `CAP_NET_ADMIN`); `user` and a pre-created `--tap` work
everywhere.

In `cni` mode, the guest NIC uses the MAC CNI assigned to `eth0` because `macspoofchk` allow-lists
that address; the SMBIOS ROM identity remains unchanged.

Auto-created devices carry cocoon-macos's own host name family (`net_scope` `cm`: TAPs
`cm<vmid8>-<nic>`, netns `cm-<vmid>`), so a cocoon daemon's GC on the same node never reads a live
macOS guest's TAP as an orphan (see cocoon's `net_scope` in its networking docs).

### `--net cni` and TC redirect

CNI runs QEMU inside a per-VM network namespace. cocoon's CNI wires the netns veth to the QEMU TAP
with **TC ingress redirect** (`mirred`), not a bridge — the guest's DHCP/traffic flows
tap → eth0 → `cni0` → upstream, and it comes back the same way. The guest gets a real ToR IP; SSH
goes straight to that IP (no port-forward).

`--cni-conf-dir` (default `/etc/cni/net.d`) and `--cni-bin-dir` (default
`/opt/cni/bin`) point at a non-standard CNI installation; both are ignored by
the other net modes.

## VNC exposure

VNC exposure depends on the net mode:

- **`--net user | tap | bridge`** — QEMU binds VNC **loopback-only** (`127.0.0.1:590<vnc>`). Reach it
  by tunnelling: `ssh -L 5901:127.0.0.1:5901 <host>`.
- **`--net cni`** — QEMU runs in a netns, so its `127.0.0.1` bind would be unreachable off-box.
  Instead VNC rides a unix socket fronted by a small host-side proxy on **all interfaces**
  (`0.0.0.0:590<vnc>`). Because that is reachable off the host, **`--vnc-password` is required** for
  `--net cni` — the launch is rejected without it.

VNC is **launch-scoped**: it is off unless `--vnc` is given for that launch, it is cleared on
`vm stop`, and it is re-enabled per start:

```bash
cocoon-macos vm start m1 --vnc 1 --vnc-password s3cret   # this run only
cocoon-macos vm stop  m1                                 # VNC gone with the qemu it belonged to
```

The VNC password is never written to disk — it is read from the flag on each start.

### macOS Screen Sharing

QEMU's default `None` auth **hangs macOS Screen Sharing**. Pass `--vnc-password <≤8 chars>` (applied
via the QEMU monitor post-launch) so Screen Sharing prompts and connects. The launch is **rejected**
(not truncated) if the password exceeds 8 characters or contains control characters — QEMU's VNC DES
auth only supports 8-byte passwords. Plain VNC clients (RealVNC/TigerVNC) work without a password on
the loopback modes.

> In-guest macOS Screen Sharing (to the guest's own IP) is **not** enabled headlessly — macOS
> requires the Screen Recording TCC grant from the GUI or MDM. Use QEMU's built-in VNC above instead.

## Display sleep blanks VNC

macOS only repaints the emulated framebuffer while the display is **awake**; once it sleeps (~idle),
VNC shows a blank white/black screen with just the cursor even though the guest is healthy (SSH works,
WindowServer is up). It is *not* a GPU/driver problem — a mouse move repaints it. The golden image's
first-boot daemon runs `pmset -a displaysleep 0 disablesleep 1` system-wide (covering the pre-login
loginwindow) to keep the framebuffer painted; older images need a `setup`-stage rebuild. See also the
GPU/video note in [Boot, Firmware & GUI](vm.md).
