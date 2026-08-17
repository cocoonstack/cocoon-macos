package image

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"golang.org/x/sync/errgroup"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/registry"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/credentials"

	"github.com/cocoonstack/cocoon/utils"
)

// pullConns is the parallel HTTP Range connection count; ghcr throttles a single stream to a fraction of the link.
const pullConns = 8

const (
	artifactTypeOSImage = "application/vnd.cocoonstack.os-image.v1+json"
	mediaTypeDiskQcow2  = "application/vnd.cocoonstack.disk.qcow2"
)

// pullOCIBlob downloads ref's qcow2 layer to dest and verifies its sha256 digest.
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
	// flush before import adopts the file, so a crash can't leave an unverified blob in the store
	if err = f.Sync(); err != nil {
		return fmt.Errorf("sync %s: %w", dest, err)
	}
	return verifyDigest(dest, layer.Digest.String())
}

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

// rangeSupported probes whether the blob endpoint honors Range (ghcr's presigned redirect does; a registry answering 200 does not).
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

func fetchSingle(ctx context.Context, repo *remote.Repository, layer ocispec.Descriptor, f *os.File) error {
	rc, err := repo.Blobs().Fetch(ctx, layer)
	if err != nil {
		return fmt.Errorf("fetch layer %s: %w", layer.Digest, err)
	}
	defer func() { _ = rc.Close() }()
	_, err = io.Copy(f, rc)
	return err
}

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

// dockerCredential resolves credentials from the user's docker config; a missing config yields an empty store, so anonymous public pulls still work.
func dockerCredential() auth.CredentialFunc {
	store, err := credentials.NewStoreFromDocker(credentials.StoreOptions{})
	if err != nil {
		return auth.StaticCredential("", auth.EmptyCredential)
	}
	return credentials.Credential(store)
}

// ValidatePushReference rejects digest destinations before an export stops the VM because the new manifest must be published under a tag.
func ValidatePushReference(ref string) error {
	_, _, err := parsePushReference(ref)
	return err
}

// PushCloudImage publishes path as a single-layer Cocoon cloud-image artifact.
func PushCloudImage(ctx context.Context, ref, path string, annotations map[string]string) (ocispec.Descriptor, error) {
	repoRef, tag, err := parsePushReference(ref)
	if err != nil {
		return ocispec.Descriptor{}, err
	}
	repo, err := remote.NewRepository(repoRef)
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("open repository %s: %w", repoRef, err)
	}
	repo.Client = &auth.Client{Cache: auth.NewCache(), Credential: dockerCredential()}
	title := filepath.Base(repoRef) + ".qcow2"
	return pushCloudImage(ctx, repo, tag, path, title, annotations)
}

func parsePushReference(ref string) (repo, tag string, err error) {
	parsed, err := registry.ParseReference(ref)
	if err != nil {
		return "", "", fmt.Errorf("parse OCI destination %q: %w", ref, err)
	}
	tag = parsed.ReferenceOrDefault()
	parsed.Reference = tag
	if err := parsed.ValidateReferenceAsTag(); err != nil {
		return "", "", fmt.Errorf("OCI destination %q must use a tag: %w", ref, err)
	}
	return parsed.Registry + "/" + parsed.Repository, tag, nil
}

func pushCloudImage(ctx context.Context, target oras.Target, tag, path, title string, annotations map[string]string) (ocispec.Descriptor, error) {
	f, err := os.Open(path) //nolint:gosec // local VM export path
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("open cloud image: %w", err)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("stat cloud image: %w", err)
	}
	dgst, err := digest.FromReader(f)
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("hash cloud image: %w", err)
	}
	layer := ocispec.Descriptor{
		MediaType: mediaTypeDiskQcow2,
		Digest:    dgst,
		Size:      info.Size(),
		Annotations: map[string]string{
			ocispec.AnnotationTitle: title,
		},
	}
	exists, err := target.Exists(ctx, layer)
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("check cloud image blob: %w", err)
	}
	if !exists {
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return ocispec.Descriptor{}, fmt.Errorf("seek cloud image: %w", err)
		}
		if err := target.Push(ctx, layer, f); err != nil {
			return ocispec.Descriptor{}, fmt.Errorf("push cloud image blob: %w", err)
		}
	}

	manifestAnnotations := maps.Clone(annotations)
	if manifestAnnotations == nil {
		manifestAnnotations = map[string]string{}
	}
	manifestAnnotations["cocoonstack.disk.format"] = "qcow2"
	manifestDesc, err := oras.PackManifest(ctx, target, oras.PackManifestVersion1_1, artifactTypeOSImage, oras.PackManifestOptions{
		Layers:              []ocispec.Descriptor{layer},
		ManifestAnnotations: manifestAnnotations,
	})
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("pack cloud image manifest: %w", err)
	}
	if err := target.Tag(ctx, manifestDesc, tag); err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("tag cloud image manifest: %w", err)
	}
	return manifestDesc, nil
}

// pickQcow2Layer prefers the layer whose title annotation ends in .qcow2 (what `oras push` writes), else the largest.
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
