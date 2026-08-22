package image

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteExportFileReplacesOutputAtomically(t *testing.T) {
	dir := t.TempDir()
	output := filepath.Join(dir, "image.qcow2")
	if err := os.WriteFile(output, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeExportFile(output, strings.NewReader("new image")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); got != "new image" {
		t.Fatalf("output = %q, want %q", got, "new image")
	}
	info, err := os.Stat(output)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o644 {
		t.Fatalf("mode = %o, want 644", got)
	}
}

func TestWriteExportFilePreservesExistingOutputOnFailure(t *testing.T) {
	dir := t.TempDir()
	output := filepath.Join(dir, "image.qcow2")
	if err := os.WriteFile(output, []byte("valid image"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := writeExportFile(output, io.MultiReader(strings.NewReader("partial"), failingReader{}))
	if err == nil {
		t.Fatal("expected copy failure")
	}
	data, readErr := os.ReadFile(output)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if got := string(data); got != "valid image" {
		t.Fatalf("existing output changed after failure: %q", got)
	}
	matches, globErr := filepath.Glob(filepath.Join(dir, ".image.qcow2.tmp-*"))
	if globErr != nil {
		t.Fatal(globErr)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary files leaked: %v", matches)
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) {
	return 0, errors.New("injected read failure")
}
