//go:build !darwin

package clone

import (
	"errors"
	"fmt"
	"runtime"
)

// privateSizes is whether this platform reports private sizes: off darwin there is no APFS.
const privateSizes = false

// privateSize returns an error wrapping errors.ErrUnsupported: off darwin there is no APFS private
// size to read.
func privateSize(path string) (int64, error) {
	return 0, fmt.Errorf("reading the private size of %s on %s: %w", path, runtime.GOOS,
		errors.ErrUnsupported)
}
