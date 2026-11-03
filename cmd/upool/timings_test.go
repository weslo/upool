package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/weslo/upool/internal/ledger"
	"github.com/weslo/upool/internal/output"
	"github.com/weslo/upool/internal/pool"
	"github.com/weslo/upool/internal/timing"
	"github.com/weslo/upool/internal/unity/fake"
)

// The acceptance tests of #266: each operation that moves a slot or an Editor reports how long its
// steps took, in its envelope's timings under --json and as one line on stderr without it, and
// appends one line to its pool's timings log, ~/.upool/pools/<repo-key>/timings.jsonl, failed or
// not. The test clock stands still, so most steps take 0 ms; the open and close tests run on
// testing/synctest's clock, whose waits give their steps real durations.

// logLine is one line of the timings log, every field of it.
type logLine struct {
	Time      time.Time     `json:"time"`
	Operation string        `json:"operation"`
	Slot      string        `json:"slot"`
	OK        bool          `json:"ok"`
	Steps     []output.Step `json:"steps"`
	TotalMS   int64         `json:"total_ms"`
}

// timingsLog returns the lines of the timings log of the pool key under home, none when it does
// not exist, failing the test on a line that is not one of the log's.
func timingsLog(t *testing.T, home, key string) []logLine {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(home, ".upool", "pools", key, timing.LogFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var lines []logLine
	scan := bufio.NewScanner(bytes.NewReader(b))
	for scan.Scan() {
		dec := json.NewDecoder(bytes.NewReader(scan.Bytes()))
		dec.DisallowUnknownFields()
		var l logLine
		if err := dec.Decode(&l); err != nil {
			t.Fatalf("timings log line %q: %v", scan.Text(), err)
		}
		lines = append(lines, l)
	}
	return lines
}

// lastLogged returns the harness pool's timings log's last line, failing the test unless the log
// has want lines.
func (p harnessPool) lastLogged(t *testing.T, want int) logLine {
	t.Helper()
	lines := timingsLog(t, p.Home, p.key)
	if len(lines) != want {
		t.Fatalf("the timings log has %d lines, %+v; want %d", len(lines), lines, want)
	}
	return lines[len(lines)-1]
}

// checkLogged fails the test unless l is the log line of an operation that started at the test
// clock, succeeded if ok, and reported the timings reported.
func checkLogged(t *testing.T, l logLine, reported output.Timings, ok bool) {
	t.Helper()
	got := output.Timings{Operation: l.Operation, Slot: l.Slot, Steps: l.Steps,
		TotalMS: l.TotalMS}
	if l.OK != ok || !l.Time.Equal(clock) || !timingsEqual(got, reported) {
		t.Errorf("logged %+v; want ok %t, started at %s, and the timings reported, %+v", l, ok,
			clock, reported)
	}
}

// timingsEqual reports whether a and b are the same timings.
func timingsEqual(a, b output.Timings) bool {
	return a.Operation == b.Operation && a.Slot == b.Slot && a.TotalMS == b.TotalMS &&
		slices.Equal(a.Steps, b.Steps)
}

// envelopeTimings returns the timings of the --json envelope on stdout, failing the test unless
// it has some and stderr has no line of them.
func envelopeTimings(t *testing.T, r result) output.Timings {
	t.Helper()
	env := decode(t, r.stdout)
	if env.Timings == nil || r.timings != "" {
		t.Fatalf("envelope %+v, timings line %q; want timings in the envelope and no line of "+
			"them on stderr", env, r.timings)
	}
	return *env.Timings
}

// zeroSteps are steps by name, each taking 0 ms, as on the test clock.
func zeroSteps(names ...string) []output.Step {
	steps := []output.Step{}
	for _, n := range names {
		steps = append(steps, output.Step{Name: n})
	}
	return steps
}

// take prints only the slot's path on stdout, and its timings, the handout, on one line of
// stderr; under --json they are in the envelope. Each take logs one line.
func TestTimingsOfTake(t *testing.T) {
	p := newHarnessPool(t)
	r := runIn(t, p.Env, p.Repo, "take", "feature-x")
	line := timingsPrefix + "take slot-1: handout 0 ms; total 0 ms"
	if r.code != output.ExitOK || r.stdout != p.slot("slot-1")+"\n" || r.stderr != "" ||
		r.timings != line {
		t.Errorf("exit %d, stdout %q, stderr %q, timings %q; want 0, slot-1's path alone, and "+
			"stderr the line %q alone", r.code, r.stdout, r.stderr, r.timings, line)
	}
	want := output.Timings{Operation: "take", Slot: "slot-1", Steps: zeroSteps("handout")}
	checkLogged(t, p.lastLogged(t, 1), want, true)

	r = runIn(t, p.Env, p.Repo, "take", "feature-y", "--json")
	got := envelopeTimings(t, r)
	want = output.Timings{Operation: "take", Slot: "slot-2", Steps: zeroSteps("handout")}
	if !timingsEqual(got, want) {
		t.Errorf("--json: timings %+v, want %+v", got, want)
	}
	checkLogged(t, p.lastLogged(t, 2), want, true)
}

// A take that leases nothing still reports and logs its timings, with no steps, as failed.
func TestTimingsOfATakeThatFails(t *testing.T) {
	p := newHarnessPool(t)
	runIn(t, p.Env, p.Repo, "take", "feature-x")
	runIn(t, p.Env, p.Repo, "take", "feature-y")
	r := runIn(t, p.Env, p.Repo, "take", "feature-z", "--no-create")
	line := timingsPrefix + "take: total 0 ms"
	if r.code != output.ExitFailure || r.stdout != "" || r.timings != line {
		t.Errorf("exit %d, stdout %q, timings %q; want 1, nothing, and %q", r.code, r.stdout,
			r.timings, line)
	}
	checkLogged(t, p.lastLogged(t, 3), output.Timings{Operation: "take",
		Steps: zeroSteps()}, false)
}

// A command line refused before it finds a pool reports no timings and logs nothing.
func TestTimingsOutsideAPool(t *testing.T) {
	p := newHarnessPool(t)
	outside := tempDir(t)
	for _, args := range [][]string{{"take", "feature-x"}, {"release"}, {"refresh"}, {"open"},
		{"close"}, {"take"}} {
		r := runIn(t, p.Env, outside, args...)
		if r.code == output.ExitOK || r.timings != "" {
			t.Errorf("%q outside the pool: exit %d, timings %q; want a failure and none", args,
				r.code, r.timings)
		}
	}
	if lines := timingsLog(t, p.Home, p.key); len(lines) != 0 {
		t.Errorf("the timings log has %+v, want nothing", lines)
	}
}

// `upool run` times nothing of the take and release it makes around its command, and logs
// nothing: the timings are the operations', and run is not one (#266).
func TestTimingsNotOfRun(t *testing.T) {
	p := newHarnessPool(t)
	r := runSh(t, p, "feature-x", "true")
	if r.code != output.ExitOK || r.timings != "" {
		t.Errorf("run: exit %d, stderr %q, timings %q; want 0 and no timings", r.code, r.stderr,
			r.timings)
	}
	if lines := timingsLog(t, p.Home, p.key); len(lines) != 0 {
		t.Errorf("the timings log has %+v, want nothing", lines)
	}
}

// release times its verify, status, reset and mirror; with no unity on PATH the verify verifies
// nothing, and is still a step.
func TestTimingsOfRelease(t *testing.T) {
	p := takenPool(t)
	r := runIn(t, p.Env, p.slot("slot-1"), "release")
	line := timingsPrefix + "release slot-1: verify 0 ms, status 0 ms, reset 0 ms, mirror 0 ms; " +
		"total 0 ms"
	if r.code != output.ExitOK || r.timings != line {
		t.Errorf("exit %d, stderr %q, timings %q; want 0 and %q", r.code, r.stderr, r.timings,
			line)
	}
	checkLogged(t, p.lastLogged(t, 2), output.Timings{Operation: "release", Slot: "slot-1",
		Steps: zeroSteps("verify", "status", "reset", "mirror")}, true)
}

// release with the Editor upool opened in the slot times its save_all and close first, under
// --json; a dirty slot is parked, with no reset step.
func TestTimingsOfReleaseWithAnEditor(t *testing.T) {
	p := takenPool(t)
	p.installUnity(t, saveAllReply, closeReply, p.verifyReply("projects-verify-pass.json", 0))
	writeLedger(t, p, slot1Editor(p, true))
	if err := os.WriteFile(filepath.Join(p.slot("slot-1"), "Assets", "new.txt"), nil,
		0o644); err != nil {
		t.Fatal(err)
	}
	procs := &dyingStart{editorProcs: procsAlive(), pid: closedPID, starts: 1}
	r := runProcs(t, p.Env, p.Repo, procs, func() time.Time { return clock }, "release",
		"feature-x", "--json")
	got := envelopeTimings(t, r)
	want := output.Timings{Operation: "release", Slot: "slot-1",
		Steps: zeroSteps("save_all", "close", "verify", "status", "mirror")}
	if r.code != output.ExitOK || !timingsEqual(got, want) {
		t.Errorf("exit %d, timings %+v; want 0 and %+v", r.code, got, want)
	}
	p.checkReleased(t, pool.StateParked)
	checkLogged(t, p.lastLogged(t, 2), want, true)
}

// refresh names the slot of each step but the mirror: here the reset of a needs-reset slot. The
// operation names no slot.
func TestTimingsOfRefresh(t *testing.T) {
	p := newHarnessPool(t)
	p.update(t, func(pl *pool.Pool) { pl.Slots[1].State = pool.StateNeedsReset })
	r := runIn(t, p.Env, p.Repo, "refresh")
	line := timingsPrefix + "refresh: reset slot-2 0 ms, mirror 0 ms; total 0 ms"
	if r.code != output.ExitOK || r.timings != line {
		t.Errorf("exit %d, stderr %q, timings %q; want 0 and %q", r.code, r.stderr, r.timings,
			line)
	}
	checkLogged(t, p.lastLogged(t, 1), output.Timings{Operation: "refresh",
		Steps: []output.Step{{Name: "reset", Slot: "slot-2"}, {Name: "mirror"}}}, true)
}

// refresh --fetch times the move of a slot to origin/<base> and its re-warm as one step each,
// naming the slot; the re-warm's open and close are no steps of their own.
func TestTimingsOfRefreshMoveAndRewarm(t *testing.T) {
	p, origin := fetchPool(t)
	pushUpstream(t, p, origin)
	p.installUnity(t, rewarmReplies(t, p, nil)...)
	r := runRefresh(t, p, newRewarmProcs(1, nil), "--fetch", "--json")
	got := envelopeTimings(t, r)
	want := output.Timings{Operation: "refresh", Steps: []output.Step{{Name: "mirror"},
		{Name: "move", Slot: "slot-1"}, {Name: "rewarm", Slot: "slot-1"}}}
	if r.code != output.ExitOK || !timingsEqual(got, want) {
		t.Errorf("exit %d, timings %+v; want 0 and %+v", r.code, got, want)
	}
	checkLogged(t, p.lastLogged(t, 1), want, true)
}

// open times its admission, here two seconds of waiting for a running cold import to end, and
// ready, from `unity open` to the Editor ready; under --json, a warm open the same.
func TestTimingsOfOpen(t *testing.T) {
	p := newHarnessPool(t)
	p.installUnity(t, openReply(t, p.Env, "open-headless.json"), statusReady)
	writeLedger(t, p, ledger.Editor{Project: p.slot("slot-2"), PID: 1001, Mode: modeHeadless,
		OpenedByUpool: true, ColdImport: true, ChargeGB: 4.5})
	procs := &dyingStart{editorProcs: procsAlive(os.Getpid(), capturedPID), pid: 1001, starts: 2}
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		r := runOpen(t, p, procs, time.Now)
		line := timingsPrefix + "open slot-1: admit 2000 ms, ready 0 ms; total 2000 ms"
		if r.code != output.ExitOK || r.timings != line {
			t.Errorf("exit %d, stderr %q, timings %q; want 0 and %q", r.code, r.stderr,
				r.timings, line)
		}
		l := p.lastLogged(t, 1)
		if !l.Time.Equal(start.UTC()) {
			t.Errorf("logged at %s, want %s, when open started", l.Time, start.UTC())
		}
	})

	p = warmPool(t)
	p.installUnity(t, openReply(t, p.Env, "open-headless.json"), statusReady)
	r := runOpen(t, p, procsAlive(os.Getpid(), capturedPID), func() time.Time { return clock },
		"--json")
	got := envelopeTimings(t, r)
	want := output.Timings{Operation: "open", Slot: "slot-1", Steps: zeroSteps("admit", "ready")}
	if r.code != output.ExitOK || !timingsEqual(got, want) {
		t.Errorf("--json: exit %d, timings %+v; want 0 and %+v", r.code, got, want)
	}
	checkLogged(t, p.lastLogged(t, 1), want, true)
}

// close times save_all and close, which waits here two seconds for the Editor's process to go.
func TestTimingsOfClose(t *testing.T) {
	p, _, _ := closePool(t, saveAllReply, closeReply)
	procs := &dyingStart{editorProcs: procsAlive(), pid: closedPID, starts: 2}
	synctest.Test(t, func(t *testing.T) {
		r := runInSlot1(t, p, procs, time.Now, "close", "--json")
		got := envelopeTimings(t, r)
		want := output.Timings{Operation: "close", Slot: "slot-1", Steps: []output.Step{
			{Name: "save_all"}, {Name: "close", MS: 2000}}, TotalMS: 2000}
		if r.code != output.ExitOK || !timingsEqual(got, want) {
			t.Errorf("exit %d, timings %+v; want 0 and %+v", r.code, got, want)
		}
		if l := p.lastLogged(t, 1); !l.OK || !timingsEqual(output.Timings{Operation: l.Operation,
			Slot: l.Slot, Steps: l.Steps, TotalMS: l.TotalMS}, want) {
			t.Errorf("logged %+v, want ok and %+v", l, want)
		}
	})
}

// A close whose `unity close` fails still reports and logs the step it completed, save_all, as
// failed.
func TestTimingsOfACloseThatFails(t *testing.T) {
	p, _, _ := closePool(t, saveAllReply, fake.Reply{Capture: "version.raw.txt"})
	r := runInSlot1(t, p, procsAlive(closedPID), func() time.Time { return clock }, "close")
	line := timingsPrefix + "close slot-1: save_all 0 ms; total 0 ms"
	if r.code != output.ExitFailure || r.timings != line {
		t.Errorf("exit %d, stderr %q, timings %q; want 1 and %q", r.code, r.stderr, r.timings,
			line)
	}
	checkLogged(t, p.lastLogged(t, 1), output.Timings{Operation: "close", Slot: "slot-1",
		Steps: zeroSteps("save_all")}, false)
}
