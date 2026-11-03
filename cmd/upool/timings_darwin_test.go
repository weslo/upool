//go:build darwin

package main

import (
	"testing"

	"github.com/weslo/upool/internal/output"
	"github.com/weslo/upool/internal/pool"
)

// The acceptance tests of #266 that need macOS: take --confirm, which records its holder's start
// time, and a take that clones, as the clone recipe does only there.

// take --confirm times the deferred reset of a slot handed out from needs-reset, then the confirm;
// under --json, a slot handed out ready has the confirm alone.
func TestTimingsOfConfirm(t *testing.T) {
	p := newHarnessPool(t)
	p.update(t, func(pl *pool.Pool) {
		pl.Slots[0].State = pool.StateNeedsReset
		pl.Slots[1].State = pool.StateParked
	})
	s := p.hookLease(t)
	r := p.confirmSlot(t, s.Path, s.Path)
	line := timingsPrefix + "take --confirm slot-1: reset 0 ms, confirm 0 ms; total 0 ms"
	if r.stderr != "" || r.timings != line {
		t.Errorf("stderr %q, timings %q; want only %q", r.stderr, r.timings, line)
	}
	checkLogged(t, p.lastLogged(t, 1), output.Timings{Operation: "take --confirm",
		Slot: "slot-1", Steps: zeroSteps("reset", "confirm")}, true)

	p = newHarnessPool(t)
	s = p.hookLease(t)
	r = runIn(t, p.Env, p.Repo, "take", "--confirm", s.Path, "--json")
	got := envelopeTimings(t, r)
	want := output.Timings{Operation: "take --confirm", Slot: s.Name,
		Steps: zeroSteps("confirm")}
	if r.code != output.ExitOK || !timingsEqual(got, want) {
		t.Errorf("exit %d, timings %+v; want 0 and %+v", r.code, got, want)
	}
	checkLogged(t, p.lastLogged(t, 1), want, true)
}

// A take that clones a slot times the clone, naming the slot it made, then the handout, and its
// stdout is still the path alone.
func TestTimingsOfATakeThatClones(t *testing.T) {
	e := leasedPool(t)
	r := runIn(t, e, e.Repo, "take", "feature-y")
	slot2 := e.Path(".claude/worktrees/slot-2")
	line := timingsPrefix + "take slot-2: clone slot-2 0 ms, handout 0 ms; total 0 ms"
	if r.code != output.ExitOK || r.stdout != slot2+"\n" || r.timings != line {
		t.Fatalf("exit %d, stdout %q, stderr %q, timings %q; want 0, slot-2's path alone, and "+
			"%q", r.code, r.stdout, r.stderr, r.timings, line)
	}
	lines := timingsLog(t, e.Home, repoKey(t, e))
	if len(lines) != 2 {
		t.Fatalf("the timings log has %+v, want the first take's line and this one's", lines)
	}
	checkLogged(t, lines[1], output.Timings{Operation: "take", Slot: "slot-2",
		Steps: []output.Step{{Name: "clone", Slot: "slot-2"}, {Name: "handout"}}}, true)
}
