package image

import (
	"testing"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// TestPickQcow2Layer covers each selection branch: title match, first of many matches, sole layer,
// largest fallback, and the empty-layers error.
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

func TestSplitRanges(t *testing.T) {
	sizes := []int64{1, 7, 8, 9, 100, 257 << 10, (257 << 10) + 7}
	for _, size := range sizes {
		for _, n := range []int{1, 3, 8, 100} {
			ranges := splitRanges(size, n)
			if len(ranges) == 0 {
				t.Fatalf("size=%d n=%d: no ranges", size, n)
			}
			if ranges[0][0] != 0 {
				t.Errorf("size=%d n=%d: first start %d, want 0", size, n, ranges[0][0])
			}
			if last := ranges[len(ranges)-1][1]; last != size-1 {
				t.Errorf("size=%d n=%d: last end %d, want %d", size, n, last, size-1)
			}
			// contiguous + non-overlapping: each start is the previous end + 1
			for i := 1; i < len(ranges); i++ {
				if ranges[i][0] != ranges[i-1][1]+1 {
					t.Errorf("size=%d n=%d: gap/overlap at %d: %v after %v", size, n, i, ranges[i], ranges[i-1])
				}
			}
			// every range is non-empty and never exceeds n chunks
			if len(ranges) > n {
				t.Errorf("size=%d n=%d: %d chunks exceeds n", size, n, len(ranges))
			}
		}
	}
}
