package image

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"testing"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/content/memory"
)

type closingTarget struct {
	oras.Target
}

func (t closingTarget) Push(ctx context.Context, expected ocispec.Descriptor, reader io.Reader) error {
	err := t.Target.Push(ctx, expected, reader)
	if closer, ok := reader.(io.Closer); ok {
		_ = closer.Close()
	}
	return err
}

func TestPushCloudImageManifest(t *testing.T) {
	path := t.TempDir() + "/disk.qcow2"
	if err := os.WriteFile(path, []byte("qcow2-data"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := memory.New()
	if _, err := pushCloudImage(t.Context(), store, "v1", path, "macos.qcow2", map[string]string{"cocoonstack.os.name": "macos"}); err != nil {
		t.Fatal(err)
	}
	desc, err := store.Resolve(t.Context(), "v1")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := content.FetchAll(t.Context(), store, desc)
	if err != nil {
		t.Fatal(err)
	}
	var got ocispec.Manifest
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.ArtifactType != artifactTypeOSImage || len(got.Layers) != 1 {
		t.Fatalf("artifact=%q layers=%d", got.ArtifactType, len(got.Layers))
	}
	if got.Layers[0].MediaType != mediaTypeDiskQcow2 || got.Layers[0].Annotations[ocispec.AnnotationTitle] != "macos.qcow2" {
		t.Fatalf("unexpected layer: %+v", got.Layers[0])
	}
}

func TestPushCloudImageAllowsTargetToCloseReader(t *testing.T) {
	path := t.TempDir() + "/disk.qcow2"
	if err := os.WriteFile(path, []byte("qcow2-data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := pushCloudImage(t.Context(), closingTarget{Target: memory.New()}, "v1", path, "macos.qcow2", nil); err != nil {
		t.Fatal(err)
	}
}

func TestValidatePushReferenceRejectsDigest(t *testing.T) {
	err := ValidatePushReference("registry.example.com/team/macos@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err == nil {
		t.Fatal("expected digest destination to be rejected")
	}
}

func TestParsePushReferenceDefaultsToLatest(t *testing.T) {
	repo, tag, err := parsePushReference("registry.example.com/team/macos")
	if err != nil {
		t.Fatal(err)
	}
	if repo != "registry.example.com/team/macos" || tag != "latest" {
		t.Fatalf("repo = %q, tag = %q", repo, tag)
	}
}
