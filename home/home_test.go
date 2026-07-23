package home

import (
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"

	"github.com/cocoonstack/cocoon/images"
	"github.com/cocoonstack/cocoon/images/cloudimg"
	metajson "github.com/cocoonstack/cocoon/meta/json"
	"github.com/cocoonstack/cocoon/meta/tombstone"
)

// TestCloudimgNamespace pins the namespace to cocoon's layout: a drifting key
// or path would leave cocoon-written state unreadable here.
func TestCloudimgNamespace(t *testing.T) {
	root := t.TempDir()
	ns := cloudimgNamespace(root)
	conf := cloudimg.NewConfig(root, 0)

	if ns.Name != cloudimg.NamespaceName {
		t.Errorf("name = %q, want %q", ns.Name, cloudimg.NamespaceName)
	}
	if ns.FilePath != conf.IndexFile() {
		t.Errorf("file = %q, want %q", ns.FilePath, conf.IndexFile())
	}
	if ns.LockPath != conf.IndexLock() {
		t.Errorf("lock = %q, want %q", ns.LockPath, conf.IndexLock())
	}
	if got, want := filepath.Dir(ns.FilePath), filepath.Dir(ns.LockPath); got != want {
		t.Errorf("file dir %q != lock dir %q", got, want)
	}

	codec, ok := ns.Codec.(metajson.TableCodec)
	if !ok {
		t.Fatalf("codec type = %T, want metajson.TableCodec", ns.Codec)
	}
	want := []metajson.TableSpec{
		{Key: "images", Table: images.TableRecords},
		{Key: "tombstones", Table: tombstone.TableName, Optional: true},
	}
	if len(codec.Specs) != len(want) {
		t.Fatalf("specs = %v, want %v", codec.Specs, want)
	}
	for i, w := range want {
		if codec.Specs[i] != w {
			t.Errorf("spec[%d] = %+v, want %+v", i, codec.Specs[i], w)
		}
	}
}

// TestOpenStoreEmpty covers the full OpenStore path on a fresh state dir.
func TestOpenStoreEmpty(t *testing.T) {
	cmd := newTestCmd(t)
	ctx, store, err := OpenStore(cmd)
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

func newTestCmd(t *testing.T) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{}
	cmd.Flags().String("state-dir", t.TempDir(), "")
	cmd.SetContext(t.Context())
	return cmd
}
