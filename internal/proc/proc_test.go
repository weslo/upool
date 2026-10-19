package proc

import (
	"math"
	"testing"
	"time"
)

func TestStartRejectsInvalidPID(t *testing.T) {
	for _, pid := range []int{0, -1, math.MaxInt32 + 1} {
		if _, err := Start(pid); err == nil {
			t.Errorf("Start(%d) error = nil, want an error", pid)
		}
		if alive, err := Alive(pid, time.Time{}); err == nil {
			t.Errorf("Alive(%d) = %v, nil, want an error", pid, alive)
		}
	}
}
