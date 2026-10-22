package clone

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"runtime"
)

// Disk is a checkout's disk use, as status --du reports it (docs/contracts.md A32; docs/design.md,
// "Disk: 0.4 GB per persistent slot; disk is not what limits the count").
type Disk struct {
	// Private is the sum of its regular files' APFS private sizes, the bytes no other clone
	// shares: what deleting the checkout frees. `du` counts a clone's shared blocks in full, so it
	// overstates this.
	Private int64
	// Apparent is the sum of their sizes, which for a clone says nothing of what it costs.
	Apparent int64
}

// DiskUse sums the private and apparent sizes of the regular files under dir, without following
// symbolic links, with one getattrlist call per file (A32). A file removed during the walk, as an
// Editor's Temp/ files are, is left out. Off macOS it fails with an error wrapping
// errors.ErrUnsupported, since only APFS reports a private size.
func DiskUse(dir string) (Disk, error) {
	if !privateSizes {
		return Disk{}, fmt.Errorf("reading private sizes on %s: %w", runtime.GOOS,
			errors.ErrUnsupported)
	}
	var d Disk
	err := filepath.WalkDir(dir, func(path string, e fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) && path != dir {
			return nil
		}
		if err != nil || !e.Type().IsRegular() {
			return err
		}
		info, err := e.Info()
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		private, err := privateSize(path)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		d.Private += private
		d.Apparent += info.Size()
		return nil
	})
	return d, err
}
