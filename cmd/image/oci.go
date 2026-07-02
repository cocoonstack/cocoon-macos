package image

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/credentials"
)

// pullOCILayer resolves an OCI/ghcr ref, picks its qcow2 layer, and returns an open blob reader for
// that layer streamed from the registry. The caller owns closing the reader.
func pullOCILayer(ctx context.Context, ref string) (io.ReadCloser, error) {
	repo, err := remote.NewRepository(ref)
	if err != nil {
		return nil, fmt.Errorf("parse ref %s: %w", ref, err)
	}
	repo.Client = &auth.Client{Cache: auth.NewCache(), Credential: dockerCredential()}

	desc, err := repo.Resolve(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", ref, err)
	}
	raw, err := content.FetchAll(ctx, repo.Manifests(), desc)
	if err != nil {
		return nil, fmt.Errorf("fetch manifest %s: %w", ref, err)
	}
	var manifest ocispec.Manifest
	if err = json.Unmarshal(raw, &manifest); err != nil {
		return nil, fmt.Errorf("parse manifest %s: %w", ref, err)
	}
	layer, err := pickQcow2Layer(manifest.Layers)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ref, err)
	}
	rc, err := repo.Blobs().Fetch(ctx, layer)
	if err != nil {
		return nil, fmt.Errorf("fetch layer %s: %w", layer.Digest, err)
	}
	return rc, nil
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
