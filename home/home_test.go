package home

import (
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
)

func TestOpenStoreEmpty(t *testing.T) {
	cmd := newTestCmd(t)
	ctx := t.Context()
	store, err := OpenStore(ctx, cmd)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	imgs, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(imgs) != 0 {
		t.Errorf("images = %d, want 0", len(imgs))
	}
}

func TestVMDir(t *testing.T) {
	cmd := newTestCmd(t)
	for _, tt := range []struct {
		name    string
		wantErr bool
	}{
		{name: "macos-demo"},
		{name: "", wantErr: true},
		{name: ".", wantErr: true},
		{name: "..", wantErr: true},
		{name: "../demo", wantErr: true},
		{name: "nested/demo", wantErr: true},
		{name: "/tmp/demo", wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir, err := VMDir(cmd, tt.name)
			if (err != nil) != tt.wantErr {
				t.Fatalf("VMDir(%q) error = %v, wantErr %v", tt.name, err, tt.wantErr)
			}
			if !tt.wantErr && dir != filepath.Join(Dir(cmd), "vms", tt.name) {
				t.Errorf("VMDir(%q) = %q", tt.name, dir)
			}
		})
	}
}

func newTestCmd(t *testing.T) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{}
	cmd.Flags().String("state-dir", t.TempDir(), "")
	cmd.SetContext(t.Context())
	return cmd
}
