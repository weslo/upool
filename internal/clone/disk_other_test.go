//go:build !darwin

package clone

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Off darwin there is no APFS private size, so DiskUse fails, even for a directory with no file
// to read one of.
func TestDiskUseIsUnsupportedOffDarwin(t *testing.T) {
	dir := t.TempDir()
	if _, err := DiskUse(dir); !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("DiskUse(empty) error = %v, want errors.ErrUnsupported", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := DiskUse(dir); !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("DiskUse error = %v, want errors.ErrUnsupported", err)
	}
}
