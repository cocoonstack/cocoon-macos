package image

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/credentials"

	"github.com/cocoonstack/cocoon/images/cloudimg"
	"github.com/cocoonstack/cocoon/progress"
)

// ghcr throttles a single stream to a fraction of the link.
const pullConns = 8

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
	blobURL := fmt.Sprintf("https://%s/v2/%s/blobs/%s", repo.Reference.Host(), repo.Reference.Repository, layer.Digest)

	f, err := os.Create(dest) //nolint:gosec // dest is an internal temp path (os.CreateTemp), not user input
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	digest, err := cloudimg.DownloadBlob(ctx, client, blobURL, f, pullConns, progress.Nop)
	if err != nil {
		return err
	}
	// flush before import adopts the file, so a crash can't leave an unverified blob in the store
	if err = f.Sync(); err != nil {
		return fmt.Errorf("sync %s: %w", dest, err)
	}
	if got := "sha256:" + digest; got != layer.Digest.String() {
		return fmt.Errorf("digest mismatch: got %s want %s", got, layer.Digest)
	}
	return nil
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

// dockerCredential resolves credentials from the user's docker config; a missing config yields an empty store, so anonymous public pulls still work.
func dockerCredential() auth.CredentialFunc {
	store, err := credentials.NewStoreFromDocker(credentials.StoreOptions{})
	if err != nil {
		return auth.StaticCredential("", auth.EmptyCredential)
	}
	return credentials.Credential(store)
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
