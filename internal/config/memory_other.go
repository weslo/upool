//go:build !darwin

package config

import (
	"errors"
	"fmt"
	"runtime"
)

// PhysicalMemory returns an error wrapping errors.ErrUnsupported: off darwin, upool does not read
// the machine's RAM, because Linux waits until the macOS core is dogfooded (docs/design.md, "Build
// in four phases, with the clone recipe first", the leave-out list). Tests there pass
// Inputs.Memory instead.
func PhysicalMemory() (uint64, error) {
	return 0, fmt.Errorf("physical memory on %s: %w", runtime.GOOS, errors.ErrUnsupported)
}
