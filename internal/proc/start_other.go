//go:build !darwin

package proc

import (
	"errors"
	"fmt"
	"runtime"
	"time"
)

// Start returns an error wrapping errors.ErrUnsupported: off darwin, upool does not read start
// times, because Linux waits until the macOS core is dogfooded (docs/design.md, "Build in four
// phases, with the clone recipe first", the leave-out list).
func Start(pid int) (time.Time, error) {
	if err := validPID(pid); err != nil {
		return time.Time{}, err
	}
	return time.Time{}, fmt.Errorf("process start times on %s: %w", runtime.GOOS,
		errors.ErrUnsupported)
}

// Name returns an error wrapping errors.ErrUnsupported, as Start does off darwin.
func Name(pid int) (string, error) {
	if err := validPID(pid); err != nil {
		return "", err
	}
	return "", fmt.Errorf("process names on %s: %w", runtime.GOOS, errors.ErrUnsupported)
}

// EditorsAt returns an error wrapping errors.ErrUnsupported, as Start does off darwin.
func EditorsAt(dir string) ([]int, error) {
	return nil, fmt.Errorf("the Editors of %s, on %s: %w", dir, runtime.GOOS,
		errors.ErrUnsupported)
}
