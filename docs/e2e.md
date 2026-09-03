# E2E Regression

`scripts/e2e.sh` is a testbed lifecycle regression (modeled on cocoon's), in two tiers.

## `[DUMMY]` — file-level, no macOS boot

Runs on any x86 Linux/KVM host against a tiny throwaway qcow2 (no macOS boot):

- Image store (`pull`-less `list`/`rm`).
- `vm create` overlay-on-shared-base.
- `--random-smbios` serial/MAC uniqueness + MAC == ROM.
- `snapshot`/`restore` (qcow2-internal tag rollback).
- `clone` (CoW on the shared base + distinct identity + clone-of-clone backing).
- `stop` PID-reuse-safe teardown.
- `--net bridge` auto-TAP create + `tap_owned` + teardown-on-`rm` + user-`--tap` preservation +
  negative paths.

Post-conditions assert no leaked TAPs/procs and backing-chain integrity. The `--random-smbios` rows
need `root` + `nbd` + a real `CM_OPENCORE`; the `--net` rows need `root` + a test bridge (they `SKIP`,
not `FAIL`, otherwise).

## `[REAL]` — boots the golden image

Boots `tahoe:26` from the store, passes the OpenCore picker over the HMP monitor, and asserts
SSH-ready (`sw_vers` 26.x) + in-guest serial == injected. Testbed-only.

## Running

```bash
sudo CM_BIN=./cocoon-macos CM_OPENCORE=.../OpenCore.qcow2 ./scripts/e2e.sh          # [DUMMY] (30 rows)
sudo CM_HOME=~/cm-demo CM_OPENCORE=... CM_OVMF_CODE=... CM_OVMF_VARS=... \
  ./scripts/e2e.sh --real-only
```

Args: `--real` (run `[DUMMY]` then `[REAL]`), `--real-only` (skip straight to `[REAL]`), `--no-net`
(skip the root/bridge `--net` auto-create rows), `cleanup` (tear down and exit). Env vars beyond
`CM_BIN` / `CM_HOME` / `CM_OPENCORE` / `CM_OVMF_CODE` / `CM_OVMF_VARS`: `CM_BRIDGE` (test bridge for
`--net tap|bridge`, default `cmsmoke0`), `CM_TAHOE_REF` (ghcr ref for `[REAL]`), `CM_SSH_PORT` (host
port forwarded to guest `:22` in `[REAL]`, default `2299`). The `[REAL]` tier also needs `sshpass`.

The `[DUMMY]` tier is the behavioral gate today's CI structurally can't give (CI is pure `go test` /
`vet` / build — nothing opens `/dev/kvm` or mutates netlink); it belongs on a privileged self-hosted
KVM runner. `[REAL]` stays a manual/nightly testbed run (~15 GB + cold boot).
