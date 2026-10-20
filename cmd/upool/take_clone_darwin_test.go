package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/weslo/upool/internal/config"
	"github.com/weslo/upool/internal/output"
	"github.com/weslo/upool/internal/pool"
	"github.com/weslo/upool/internal/testenv"
)

// The tests of take cloning a slot when none is ready (#62), after a real `upool init`, whose
// slot-1 a first take leases: macOS only, as the clone recipe is. max_slots is the default, 3.

// leasedPool runs init in a new environment and leases its one slot, slot-1, on feature-x.
func leasedPool(t *testing.T) *testenv.Env {
	t.Helper()
	e := newInitEnv(t)
	mustInit(t, e)
	if r := runIn(t, e, e.Repo, "take", "feature-x"); r.code != output.ExitOK {
		t.Fatalf("take feature-x: exit %d, stderr %q", r.code, r.stderr)
	}
	return e
}

// slotNamesOf returns the names of the slots in e's pool.
func slotNamesOf(t *testing.T, e *testenv.Env) []string {
	t.Helper()
	p, err := pool.Load(e.Home, repoKey(t, e))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range p.Slots {
		names = append(names, s.Name+"="+string(s.State))
	}
	return names
}

// With every slot leased and the pool below max_slots, take clones slot-2 from the main checkout,
// the only seed when no slot is ready (A19), and leases it.
func TestTakeClonesASlotFromTheMainCheckout(t *testing.T) {
	e := leasedPool(t)
	r := runIn(t, e, e.Repo, "take", "feature-y")
	slot2 := e.Path(".claude/worktrees/slot-2")
	if r.code != output.ExitOK || r.stdout != slot2+"\n" {
		t.Fatalf("exit %d, stdout %q, stderr %q; want 0 and slot-2's path", r.code, r.stdout,
			r.stderr)
	}
	for _, want := range []string{"upool: skipping slot-1: leased\n",
		"upool: no slot is ready; cloning one from " + e.Repo + ", which takes about half a " +
			"minute\n", "upool: cloned slot-2\n"} {
		if !strings.Contains(r.stderr, want) {
			t.Errorf("stderr = %q, want it to contain %q", r.stderr, want)
		}
	}
	if got := strings.Join(slotNamesOf(t, e), " "); got != "slot-1=leased slot-2=leased" {
		t.Errorf("slots = %s, want both leased", got)
	}
	if got := gitIn(t, e, slot2, "symbolic-ref", "--short", "HEAD"); got != "feature-y" {
		t.Errorf("slot-2 HEAD = %s, want feature-y", got)
	}
	if _, err := os.Stat(filepath.Join(slot2, "Library", "ScriptAssemblies")); err != nil {
		t.Errorf("slot-2 has no warm Library: %v", err)
	}
}

// With the main checkout's Editor open there is no seed: take clones nothing and says why.
func TestTakeClonesNothingWhileTheMainCheckoutsEditorIsOpen(t *testing.T) {
	e := leasedPool(t)
	e.WriteFile(testenv.EditorInstanceFile, "{}\n")
	r := runIn(t, e, e.Repo, "take", "feature-y")
	want := "upool: not cloning a slot: no seed qualifies: the main checkout: the Unity Editor " +
		"has it open (Library/EditorInstance.json exists)\n"
	if r.code != output.ExitFailure || !strings.Contains(r.stderr, want) {
		t.Errorf("exit %d, stderr %q; want 1 and %q", r.code, r.stderr, want)
	}
	if got := strings.Join(slotNamesOf(t, e), " "); got != "slot-1=leased" {
		t.Errorf("slots = %s, want slot-1 alone", got)
	}
}

// With --no-create take only waits, and fails after --timeout; at max_slots it waits the same way
// without --no-create, saying why it does not clone; and with a needs-reset slot it clones nothing
// either, since that slot is handed out before any clone (A5).
func TestTakeClonesNothingWithNoCreateOrAtMaxSlots(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, e *testenv.Env)
		args  []string
		say   string
		slots string
	}{
		{"--no-create", func(*testing.T, *testenv.Env) {}, []string{"--no-create"}, "",
			"slot-1=leased"},
		{"at max_slots", func(_ *testing.T, e *testenv.Env) {
			e.Setenv(config.EnvName("max_slots"), "1")
		}, nil, "upool: not cloning a slot: the pool has 1, its max_slots\n", "slot-1=leased"},
		{"a needs-reset slot", func(t *testing.T, e *testenv.Env) {
			err := pool.Update(t.Context(), e.Home, repoKey(t, e), func(p *pool.Pool) error {
				p.Slots[0].State, p.Slots[0].Lease = pool.StateNeedsReset, nil
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		}, nil, "upool: skipping slot-1: needs-reset\n", "slot-1=needs-reset"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := leasedPool(t)
			tt.setup(t, e)
			synctest.Test(t, func(t *testing.T) {
				start := time.Now()
				args := append([]string{"take", "feature-y", "--timeout", "2"}, tt.args...)
				r := runAt(t, e, e.Repo, time.Now, args...)
				if waited := time.Since(start); waited != 2*time.Second {
					t.Errorf("take waited %s, want 2s", waited)
				}
				end := "upool: no slot of " + e.Repo + " became ready within 2s\n"
				if r.code != output.ExitFailure || !strings.HasSuffix(r.stderr, end) ||
					!strings.Contains(r.stderr, tt.say) || strings.Count(r.stderr, "not cloning") > 1 {
					t.Errorf("exit %d, stderr %q; want 1, %q once, and %q", r.code, r.stderr,
						tt.say, end)
				}
			})
			if got := strings.Join(slotNamesOf(t, e), " "); got != tt.slots {
				t.Errorf("slots = %s, want %s", got, tt.slots)
			}
		})
	}
}
