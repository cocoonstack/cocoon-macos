package image

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"golang.org/x/sync/errgroup"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/credentials"
)

// pullConns is the number of parallel HTTP Range connections used to fetch a blob; ghcr throttles a
// single stream to a fraction of the link, so 8 chunks pull the multi-GB base images far faster.
const pullConns = 8

// pullOCIBlob resolves ref's qcow2 layer and downloads it to dest, using parallel HTTP Range
// requests when the registry supports them (falling back to a single stream otherwise) and verifying
// the content against the layer's sha256 digest.
func pullOCIBlob(ctx context.Context, ref, dest string) error {
	repo, err := remote.NewRepository(ref)
	if err != nil {
		return fmt.Errorf("parse ref %s: %w", ref, err)
	}
	client := &auth.Client{Cache: auth.NewCache(), Credential: dockerCredential()}
	repo.Client = client

	layer, err := resolveQcow2Layer(ctx, repo, ref)
	if err != nil {
		return err
	}
	blobURL := fmt.Sprintf("https://%s/v2/%s/blobs/%s", repo.Reference.Registry, repo.Reference.Repository, layer.Digest)

	f, err := os.Create(dest) //nolint:gosec // dest is an internal temp path (os.CreateTemp), not user input
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	if layer.Size <= 0 || !rangeSupported(ctx, client, blobURL) {
		if err = fetchSingle(ctx, repo, layer, f); err != nil {
			return err
		}
	} else {
		if err = f.Truncate(layer.Size); err != nil {
			return err
		}
		if err = fetchParallel(ctx, client, blobURL, layer.Size, f); err != nil {
			return err
		}
	}
	return verifyDigest(dest, layer.Digest.String())
}

// resolveQcow2Layer fetches ref's manifest and returns its qcow2 layer descriptor.
func resolveQcow2Layer(ctx context.Context, repo *remote.Repository, ref string) (ocispec.Descriptor, error) {
	desc, err := repo.Resolve(ctx, ref)
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("resolve %s: %w", ref, err)
	}
	raw, err := content.FetchAll(ctx, repo.Manifests(), desc)
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("fetch manifest %s: %w", ref, err)
	}
	var manifest ocispec.Manifest
	if err = json.Unmarshal(raw, &manifest); err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("parse manifest %s: %w", ref, err)
	}
	layer, err := pickQcow2Layer(manifest.Layers)
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("%s: %w", ref, err)
	}
	return layer, nil
}

// rangeSupported probes whether the blob endpoint honors a Range request (ghcr redirects to a
// presigned URL that does; a registry that returns 200 does not).
func rangeSupported(ctx context.Context, client *auth.Client, url string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	req.Header.Set("Range", "bytes=0-0")
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusPartialContent
}

// fetchParallel downloads [0,size) in pullConns chunks concurrently, each writing at its offset.
func fetchParallel(ctx context.Context, client *auth.Client, url string, size int64, f *os.File) error {
	g, gctx := errgroup.WithContext(ctx)
	for _, r := range splitRanges(size, pullConns) {
		start, end := r[0], r[1]
		g.Go(func() error { return fetchRange(gctx, client, url, start, end, f) })
	}
	return g.Wait()
}

// splitRanges divides [0,size) into up to n contiguous, inclusive-ended byte ranges.
func splitRanges(size int64, n int) [][2]int64 {
	chunk := (size + int64(n) - 1) / int64(n)
	var ranges [][2]int64
	for start := int64(0); start < size; start += chunk {
		end := start + chunk - 1
		if end >= size {
			end = size - 1
		}
		ranges = append(ranges, [2]int64{start, end})
	}
	return ranges
}

func fetchRange(ctx context.Context, client *auth.Client, url string, start, end int64, f *os.File) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusPartialContent {
		return fmt.Errorf("range %d-%d: unexpected status %s", start, end, resp.Status)
	}
	// A mismatched span lands bytes at the wrong offset; a short body leaves a zero
	// hole — both would only surface after verifyDigest re-hashes the whole file.
	if cr := resp.Header.Get("Content-Range"); !strings.HasPrefix(cr, fmt.Sprintf("bytes %d-%d/", start, end)) {
		return fmt.Errorf("range %d-%d: mismatched content-range %q", start, end, cr)
	}
	want := end - start + 1
	n, err := io.Copy(io.NewOffsetWriter(f, start), io.LimitReader(resp.Body, want))
	if err != nil {
		return err
	}
	if n != want {
		return fmt.Errorf("range %d-%d: short body %d of %d bytes", start, end, n, want)
	}
	return nil
}

// fetchSingle streams the blob in one connection (fallback when Range isn't supported).
func fetchSingle(ctx context.Context, repo *remote.Repository, layer ocispec.Descriptor, f *os.File) error {
	rc, err := repo.Blobs().Fetch(ctx, layer)
	if err != nil {
		return fmt.Errorf("fetch layer %s: %w", layer.Digest, err)
	}
	defer func() { _ = rc.Close() }()
	_, err = io.Copy(f, rc)
	return err
}

// verifyDigest re-hashes the downloaded file and checks it against "sha256:<hex>".
func verifyDigest(path, want string) error {
	f, err := os.Open(path) //nolint:gosec // path is an internal temp path (os.CreateTemp), not user input
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return err
	}
	got := "sha256:" + hex.EncodeToString(h.Sum(nil))
	if got != want {
		return fmt.Errorf("digest mismatch: got %s want %s", got, want)
	}
	return nil
}

// dockerCredential resolves registry credentials from the user's docker config. NewStoreFromDocker
// tolerates a missing config, so an empty store yields anonymous pulls and public ghcr images work
// without a login.
func dockerCredential() auth.CredentialFunc {
	store, err := credentials.NewStoreFromDocker(credentials.StoreOptions{})
	if err != nil {
		return auth.StaticCredential("", auth.EmptyCredential)
	}
	return credentials.Credential(store)
}

// pickQcow2Layer selects the qcow2 layer from a manifest: prefer one whose title annotation ends in
// .qcow2 (what `oras push` writes), else the sole layer, else the largest.
func pickQcow2Layer(layers []ocispec.Descriptor) (ocispec.Descriptor, error) {
	if len(layers) == 0 {
		return ocispec.Descriptor{}, fmt.Errorf("manifest has no layers")
	}
	for _, l := range layers {
		if strings.HasSuffix(l.Annotations[ocispec.AnnotationTitle], ".qcow2") {
			return l, nil
		}
	}
	if len(layers) == 1 {
		return layers[0], nil
	}
	largest := layers[0]
	for _, l := range layers[1:] {
		if l.Size > largest.Size {
			largest = l
		}
	}
	return largest, nil
}
