# cocoon-macos

macOS VM engine for x86 Linux/KVM, built on [cocoon](https://github.com/cocoonstack/cocoon). Boots real macOS guests (Sequoia 15, Tahoe 26) via [QEMU](https://www.qemu.org/) + [OpenCore](https://github.com/acidanthera/OpenCorePkg) + OVMF.

**Documentation: [cocoonstack.github.io/cocoon-macos](https://cocoonstack.github.io/cocoon-macos/)** (source in [`docs/`](docs/)).

## Highlights

- **Fully-automated macOS install** — a GitHub Action boots OpenCore, erases APFS, drives the installer by OCR, and publishes a golden qcow2 (Tahoe 26 / Sequoia 15) to ghcr; the `:26` image is **SSH-ready** (`cocoon`/`cocoon`)
- **Docker-like CLI** — `create`, `run`, `start`, `stop`, `list`, `inspect`, `console`, `rm`, `snapshot`, `restore`, `clone`
- **Parallel Range image pull** — the multi-GB qcow2 is pulled in 8 concurrent HTTP Range chunks with an sha256 digest check (oras-go for auth; no `oras` binary needed)
- **Per-VM Apple identity** — `--random-smbios` injects a unique serial/MLB/UUID/ROM into a per-VM OpenCore; the ROM is the guest MAC outside CNI, so clones never share a serial
- **CNI networking with TC redirect** — `--net cni` joins cocoon's forwarding plane so the guest DHCPs a real LAN IP; also user/tap/bridge; reachable VNC (launch-scoped, password-gated on CNI)
- **Snapshot, clone & data disks** — offline qcow2-internal snapshots, CoW clones that cold-boot a fresh Apple identity, and up to 4 extra AHCI data disks
- **Intel & AMD, built on cocoon** — one boot recipe boots both hosts; imports cocoon's `cloudimg` store, `network` plane, and copy-on-write conventions

## Quick Start

```bash
# Build, then provision the host (checks /dev/kvm + deps, installs the OpenCore + OVMF firmware)
go build -o cocoon-macos .
sudo scripts/doctor.sh

# Pull the golden image and run a VM (x86 Linux + /dev/kvm)
cocoon-macos image pull ghcr.io/cocoonstack/cocoon-macos/tahoe:26
cocoon-macos vm run ghcr.io/cocoonstack/cocoon-macos/tahoe:26 \
  --name m1 --cpus 4 --memory 8192 --ssh-port 2222 --vnc 1 --random-smbios

# Interact
ssh -p 2222 cocoon@localhost      # password: cocoon
cocoon-macos vm console m1

# Snapshot and clone
cocoon-macos vm stop m1
cocoon-macos vm snapshot m1 --tag clean
cocoon-macos vm clone m1 -n m2 --ssh-port 2223 --random-smbios

# Clean up
cocoon-macos vm rm m1 m2
```

Full walkthroughs: [Installation](docs/install.md) · [CLI reference](docs/cli.md) · [Images](docs/images.md) · [VM boot & firmware](docs/vm.md) · [Networking & VNC](docs/networking.md) · [Snapshots & clone](docs/snapshots.md) · [CI image pipeline](docs/image-pipeline.md) · [E2E regression](docs/e2e.md) · [Known issues](docs/known-issues.md) · [Roadmap](docs/roadmap.md)

## Development

```bash
make build    # Build the cocoon-macos binary
make test     # Run tests with race detector and coverage
make lint     # Run golangci-lint (GOOS=linux + darwin)
make fmt      # Format code with gofumpt + goimports
make all      # Full pipeline: deps + fmt + lint + test + build
```

## License

This project is licensed under the MIT License. See [`LICENSE`](./LICENSE).
