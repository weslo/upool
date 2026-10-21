package clone

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/weslo/upool/internal/pool"
)

// A clean needs-reset slot is reset to ready ((f) transition 20); one that is dirty, or that is
// no longer needs-reset and unleased by the time the lock is held, is left as it is, and why is
// said.
func TestResetNeedsReset(t *testing.T) {
	tests := []struct {
		name  string
		state pool.State
		// prepare changes slot-1 after the pool is made.
		prepare func(t *testing.T, e seedEnv)
		reset   bool
		why     string
		after   pool.State
	}{
		{name: "clean", state: pool.StateNeedsReset, reset: true, after: pool.StateReady},
		{name: "dirty", state: pool.StateNeedsReset, prepare: func(t *testing.T, e seedEnv) {
			file := filepath.Join(e.slot(t, "slot-1").Path, "left-behind.txt")
			if err := os.WriteFile(file, []byte("x\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}, why: "it is dirty, so it was not reset: ?? left-behind.txt",
			after: pool.StateNeedsReset},
		{name: "ready meanwhile", state: pool.StateReady, why: "it was taken meanwhile",
			after: pool.StateReady},
		{name: "leased meanwhile", state: pool.StateLeased, why: "it was taken meanwhile",
			after: pool.StateLeased},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newSeedEnv(t, []pool.State{tt.state}, []time.Time{{}})
			if tt.prepare != nil {
				tt.prepare(t, e)
			}
			reset, why, _, err := ResetNeedsReset(t.Context(), e.sd.Git,
				KeyCheck{Env: e.Environ()}, e.Home, e.key, "slot-1")
			if err != nil {
				t.Fatal(err)
			}
			if reset != tt.reset || !strings.HasPrefix(why, tt.why) ||
				(tt.why == "") != (why == "") {
				t.Errorf("ResetNeedsReset = %t, %q; want %t, %q", reset, why, tt.reset, tt.why)
			}
			if got := e.slot(t, "slot-1").State; got != tt.after {
				t.Errorf("slot-1 = %s, want %s", got, tt.after)
			}
		})
	}
}
