//go:build !darwin

package clone

import (
	"errors"
	"testing"
)

func TestCPIsUnsupportedOffDarwin(t *testing.T) {
	if err := (CP{}).Copy(t.Context(), "/src", "/dst"); !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("err = %v, want it to wrap errors.ErrUnsupported", err)
	}
}
