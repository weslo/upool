//go:build !darwin

package config

import (
	"errors"
	"testing"
)

func TestPhysicalMemoryIsUnsupportedOffDarwin(t *testing.T) {
	if _, err := PhysicalMemory(); !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("PhysicalMemory() error = %v, want one wrapping errors.ErrUnsupported", err)
	}
}
