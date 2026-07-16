package image

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"golang.org/x/sync/errgroup"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/credentials"

	"github.com/cocoonstack/cocoon/utils"
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
	// Flush before the pinned cocoon import adopts this file, so a crash right
	// after "pull complete" can't leave an unverified blob in the store.
	if err = f.Sync(); err != nil {
		return fmt.Errorf("sync %s: %w", dest, err)
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
	for _, r := range utils.SplitRanges(size, pullConns) {
		start, end := r[0], r[1]
		g.Go(func() error { return fetchRange(gctx, client, url, start, end, f) })
	}
	return g.Wait()
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
	return utils.CopyRangeBody(resp, io.NewOffsetWriter(f, start), start, end)
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
// .qcow2 (what `oras push` writes), else the largest.
func pickQcow2Layer(layers []ocispec.Descriptor) (ocispec.Descriptor, error) {
	if len(layers) == 0 {
		return ocispec.Descriptor{}, fmt.Errorf("manifest has no layers")
	}
	for _, l := range layers {
		if strings.HasSuffix(l.Annotations[ocispec.AnnotationTitle], ".qcow2") {
			return l, nil
		}
	}
	return slices.MaxFunc(layers, func(a, b ocispec.Descriptor) int { return cmp.Compare(a.Size, b.Size) }), nil
}
