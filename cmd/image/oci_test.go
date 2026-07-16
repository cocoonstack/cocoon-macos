package image

import (
	"testing"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

func TestPickQcow2Layer(t *testing.T) {
	tests := []struct {
		name    string
		layers  []ocispec.Descriptor
		wantDig string
		wantErr bool
	}{
		{
			name: "title match wins over larger untitled layer",
			layers: []ocispec.Descriptor{
				{Digest: "sha256:big", Size: 900},
				{Digest: "sha256:disk", Size: 100, Annotations: map[string]string{ocispec.AnnotationTitle: "tahoe.qcow2"}},
			},
			wantDig: "sha256:disk",
		},
		{
			name: "first qcow2 title when several match",
			layers: []ocispec.Descriptor{
				{Digest: "sha256:first", Size: 100, Annotations: map[string]string{ocispec.AnnotationTitle: "a.qcow2"}},
				{Digest: "sha256:second", Size: 999, Annotations: map[string]string{ocispec.AnnotationTitle: "b.qcow2"}},
			},
			wantDig: "sha256:first",
		},
		{
			name:    "single untitled layer",
			layers:  []ocispec.Descriptor{{Digest: "sha256:only", Size: 42}},
			wantDig: "sha256:only",
		},
		{
			name: "largest untitled fallback",
			layers: []ocispec.Descriptor{
				{Digest: "sha256:small", Size: 10},
				{Digest: "sha256:large", Size: 200},
				{Digest: "sha256:mid", Size: 50},
			},
			wantDig: "sha256:large",
		},
		{
			name:    "empty layers errors",
			layers:  nil,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := pickQcow2Layer(tt.layers)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got layer %s", got.Digest)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if string(got.Digest) != tt.wantDig {
				t.Fatalf("picked %s, want %s", got.Digest, tt.wantDig)
			}
		})
	}
}
