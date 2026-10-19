//go:build !darwin

package proc

import (
	"errors"
	"os"
	"testing"
	"time"
)

// TestStartIsUnsupported checks that off darwin, liveness is an error, never a dead process that a
// caller would reclaim.
func TestStartIsUnsupported(t *testing.T) {
	if _, err := Start(os.Getpid()); !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("Start error = %v, want errors.ErrUnsupported", err)
	}
	if alive, err := Alive(os.Getpid(), time.Now()); !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("Alive = %v, %v, want errors.ErrUnsupported", alive, err)
	}
}
