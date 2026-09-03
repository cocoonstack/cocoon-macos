# Images

## Golden images on ghcr

The CI pipeline (see [CI Image Pipeline](image-pipeline.md)) publishes two tiers per OS:

| Ref | Contents |
|-----|----------|
| `…/tahoe:26-base` | Installed macOS at the first-run Setup Assistant. |
| `…/tahoe:26` | Provisioned & **SSH-ready**: admin `cocoon`/`cocoon`, complete home, Remote Login on first boot. |

`sequoia:15` / `sequoia:15-base` are the macOS 15 (n-1) equivalents. The image is a single immutable
qcow2 blob — **qcow2-only**, no OCI layer filesystem.

## Local store

`image pull` imports the golden qcow2 into a **content-addressed store** under
`<state-dir>/cloudimg` (cocoon's `cloudimg` backend, imported directly). `vm run <ref>` then bakes a
copy-on-write overlay on the immutable shared base, so many VMs share one on-disk copy. `pull`
accepts either a registry ref or a plain http(s) URL; if the ref is already present it skips the
download unless `--force` is given.

```bash
cocoon-macos image pull ghcr.io/cocoonstack/cocoon-macos/tahoe:26
cocoon-macos image list
```

## Parallel Range download

A single HTTP/2 stream to ghcr is throttled and often reset mid-transfer on multi-GB blobs. `image
pull` therefore downloads the qcow2 layer in **8 concurrent HTTP Range chunks** to a temp file, then
verifies the SHA-256 against the layer digest before importing:

- The `oras-go` SDK resolves the manifest, credentials (from the user's Docker config; public images
  pull anonymously), and the qcow2 layer descriptor — **no external `oras` binary** is needed.
- Each chunk is fetched with a `Range` request that follows ghcr's redirect to its presigned CDN URL;
  the response's `Content-Range` and length are validated per chunk (a misbehaving CDN can't silently
  corrupt the file), and the assembled file's digest must match before import.
- If the registry doesn't support `Range`, it falls back to a single stream. The whole pull retries
  up to 3 times on transient drops.

This is both faster (parallel chunks saturate the link) and more robust than a single stream.
