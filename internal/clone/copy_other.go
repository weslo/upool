//go:build !darwin

package clone

import (
	"context"
	"errors"
	"fmt"
	"runtime"
)

// Copy returns an error wrapping errors.ErrUnsupported: off darwin there is no clonefile(2) to
// copy a seed with.
func (CP) Copy(_ context.Context, src, _ string) error {
	return fmt.Errorf("cloning %s on %s: %w", src, runtime.GOOS, errors.ErrUnsupported)
}
