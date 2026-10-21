package main

import (
	"encoding/json"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/weslo/upool/internal/output"
	"github.com/weslo/upool/internal/pool"
)

// The tests of lanes (docs/design.md, "Lanes: scripts in parallel, scenes one at a time, `.meta`
// never regenerated"): a lease is in the script lane, which any number share, or the scene lane,
// which one lease of a project holds at a time.

// takeIn runs `upool take <branch> <flags>` in p's main checkout and fails the test unless it
// leased a slot, whose lease it returns.
func takeIn(t *testing.T, p harnessPool, branch string, flags ...string) *pool.Lease {
	t.Helper()
	r := runIn(t, p.Env, p.Repo, append([]string{"take", branch}, flags...)...)
	if r.code != output.ExitOK {
		t.Fatalf("take %s %v: exit %d, stderr %q", branch, flags, r.code, r.stderr)
	}
	for _, s := range p.loadPool(t).Slots {
		if s.Lease != nil && p.slot(s.Name)+"\n" == r.stdout {
			return s.Lease
		}
	}
	t.Fatalf("take %s printed %q, no leased slot's path", branch, r.stdout)
	return nil
}

// The acceptance test of #94: two script leases coexist, by default and with --lane script, and
// which --json reports the lane.
func TestLaneScriptLeasesCoexist(t *testing.T) {
	p := newHarnessPool(t)
	for _, l := range []*pool.Lease{takeIn(t, p, "feature-a"),
		takeIn(t, p, "feature-b", "--lane", "script")} {
		if l.Lane != pool.LaneScript {
			t.Errorf("lease %s is in lane %q, want script", l.LeaseID, l.Lane)
		}
	}
	r := runIn(t, p.Env, p.slot("slot-1"), "which", "--json")
	var env struct {
		Data whichReport `json:"data"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &env); err != nil {
		t.Fatalf("which --json printed %q: %v", r.stdout, err)
	}
	if env.Data.Lease == nil || env.Data.Lease.Lane != pool.LaneScript {
		t.Errorf("which --json lease = %+v, want lane script", env.Data.Lease)
	}
}

// The acceptance test of #94: while one lease holds the scene lane, a second scene take waits as
// for a full pool, naming the first lease in its skip reasons, then fails naming it again. It
// clones no slot meanwhile: a new slot would not free the lane. The wait passes on
// testing/synctest's clock, so the test does not sleep.
func TestLaneSecondSceneTakeWaitsThenFails(t *testing.T) {
	p := newHarnessPool(t)
	first := takeIn(t, p, "feature-a", "--lane", "scene", "--holder", "bob")
	if first.Lane != pool.LaneScene {
		t.Fatalf("the first lease is in lane %q, want scene", first.Lane)
	}
	held := "the scene lane is held by bob's lease " + first.LeaseID + " in slot-1"
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		r := runAt(t, p.Env, p.Repo, time.Now, "take", "feature-b", "--lane", "scene",
			"--timeout", "3")
		if waited := time.Since(start); waited != 3*time.Second {
			t.Errorf("take waited %s, want 3s", waited)
		}
		want := "upool: skipping slot-1: leased\nupool: skipping slot-2: " + held + "\n" +
			"upool: the scene lane of " + p.Repo + " was still taken after 3s: " + held + "\n"
		if r.code != output.ExitFailure || r.stdout != "" || r.stderr != want {
			t.Errorf("exit %d, stdout %q, stderr %q; want 1, nothing, and\n%s", r.code,
				r.stdout, r.stderr, want)
		}
	})
	if got := p.slotNames(t); len(got) != 2 {
		t.Errorf("slots = %v, want slot-1 and slot-2 alone: no clone", got)
	}
}

// With no --timeout, a scene take whose lane is held fails at once, naming the lease; with no
// holder recorded, it names the lease alone.
func TestLaneSceneTakeFailsAtOnceWithoutTimeout(t *testing.T) {
	p := newHarnessPool(t)
	first := takeIn(t, p, "feature-a", "--lane", "scene")
	r := runIn(t, p.Env, p.Repo, "take", "feature-b", "--lane", "scene")
	want := "the scene lane of " + p.Repo + " is taken: the scene lane is held by lease " +
		first.LeaseID + " in slot-1"
	if r.code != output.ExitFailure || !strings.Contains(r.stderr, want) {
		t.Errorf("exit %d, stderr %q; want 1 and %q", r.code, r.stderr, want)
	}
}

// A held scene lane does not stop a script take, and a scene take is admitted once the scene
// lease is released.
func TestLaneSceneLeaseLeavesTheScriptLaneOpen(t *testing.T) {
	p := newHarnessPool(t)
	takeIn(t, p, "feature-a", "--lane", "scene")
	if l := takeIn(t, p, "feature-b"); l.Lane != pool.LaneScript {
		t.Errorf("the script take's lease is in lane %q", l.Lane)
	}
	if r := runIn(t, p.Env, p.Repo, "release", "feature-a"); r.code != output.ExitOK {
		t.Fatalf("release: exit %d, stderr %q", r.code, r.stderr)
	}
	if l := takeIn(t, p, "feature-c", "--lane", "scene"); l.Lane != pool.LaneScene {
		t.Errorf("the scene take after the release is in lane %q", l.Lane)
	}
}

// run passes --lane on to its take: the command sees its lease in the scene lane.
func TestLaneRunPassesTheLaneOn(t *testing.T) {
	p := newHarnessPool(t)
	r := runSh(t, p, "feature-a", `/bin/cat "$HOME"/.upool/pools/*/state.json`, "--lane", "scene")
	if r.code != output.ExitOK || !strings.Contains(r.stdout, `"lane": "scene"`) {
		t.Errorf("exit %d, stdout %q, stderr %q; want 0 and a lease in the scene lane", r.code,
			r.stdout, r.stderr)
	}
}

// A lane that is neither script nor scene is refused before anything is leased, and --lane has
// no place beside --confirm.
func TestLaneRefusesAnUnknownLane(t *testing.T) {
	p := newHarnessPool(t)
	r := runIn(t, p.Env, p.Repo, "take", "feature-a", "--lane", "art")
	if r.code != output.ExitFailure || !strings.Contains(r.stderr,
		`--lane "art" is neither script nor scene`) {
		t.Errorf("exit %d, stderr %q; want 1 and the lane refused", r.code, r.stderr)
	}
	p.checkNothingLeased(t)
	r = runIn(t, p.Env, p.Repo, "take", "--confirm", p.slot("slot-1"), "--lane", "scene")
	if r.code == output.ExitOK {
		t.Errorf("take --confirm with --lane: exit 0, stderr %q; want it refused", r.stderr)
	}
}
