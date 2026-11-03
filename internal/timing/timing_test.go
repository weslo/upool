package timing

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/weslo/upool/internal/output"
)

// start is when the tests' operations start.
var start = time.Date(2026, 11, 2, 9, 30, 0, 0, time.FixedZone("CET", 3600))

// ticking is a clock that starts at start and moves on by each of steps in turn, one per call
// after the first, then stands still.
func ticking(steps ...time.Duration) func() time.Time {
	now := start
	calls := 0
	return func() time.Time {
		if calls > 0 && calls <= len(steps) {
			now = now.Add(steps[calls-1])
		}
		calls++
		return now
	}
}

// poolDir makes the directory of the pool key under a new home, as its state file would, and
// returns the home.
func poolDir(t *testing.T, key string) string {
	t.Helper()
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".upool", "pools", key), 0o700); err != nil {
		t.Fatal(err)
	}
	return home
}

// logOf returns the timings log of the pool key under home.
func logOf(t *testing.T, home, key string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(home, ".upool", "pools", key, LogFile))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Each step is the time from its start to its end, and the total the whole operation; Finish
// appends the operation as one JSON line, started at its start time in UTC.
func TestTimingsOfSteps(t *testing.T) {
	home := poolDir(t, "down-3f9a1c2e")
	// Between the clock's calls: 2 ms before the clone, the clone, 3 ms, the handout, then 4 ms
	// before Finish.
	tm := Start(ticking(2*time.Millisecond, 25912*time.Millisecond, 3*time.Millisecond,
		41*time.Millisecond, 4*time.Millisecond), "take")
	tm.Pool(home, "down-3f9a1c2e")
	tm.Since("clone", "slot-3", tm.now())
	tm.Slot("slot-3")
	if err := tm.Step("handout", "", func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	got, err := tm.Finish(true)
	if err != nil {
		t.Fatal(err)
	}
	want := output.Timings{Operation: "take", Slot: "slot-3", Steps: []output.Step{
		{Name: "clone", Slot: "slot-3", MS: 25912}, {Name: "handout", MS: 41}}, TotalMS: 25962}
	if got == nil || got.Operation != want.Operation || got.Slot != want.Slot ||
		!slices.Equal(got.Steps, want.Steps) || got.TotalMS != want.TotalMS {
		t.Errorf("timings = %+v, want %+v", got, want)
	}
	line := `{"time":"2026-11-02T08:30:00Z","operation":"take","slot":"slot-3","ok":true,` +
		`"steps":[{"name":"clone","slot":"slot-3","ms":25912},{"name":"handout","ms":41}],` +
		`"total_ms":25962}` + "\n"
	if log := logOf(t, home, "down-3f9a1c2e"); log != line {
		t.Errorf("log = %q, want %q", log, line)
	}
}

// A step that fails is not recorded: an operation reports the steps it completed. Its time is in
// the total, and a failed operation is logged as one.
func TestTimingsOfAFailedStep(t *testing.T) {
	home := poolDir(t, "k")
	tm := Start(ticking(5*time.Millisecond, 7*time.Millisecond), "close")
	tm.Pool(home, "k")
	failed := errors.New("unity close failed")
	if err := tm.Step("close", "", func() error { return failed }); err != failed {
		t.Errorf("Step = %v, want fn's error", err)
	}
	got, err := tm.Finish(false)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || len(got.Steps) != 0 || got.Steps == nil || got.TotalMS != 12 {
		t.Errorf("timings = %+v, want no steps, as [], and a total of 12 ms", got)
	}
	line := `{"time":"2026-11-02T08:30:00Z","operation":"close","ok":false,"steps":[],` +
		`"total_ms":12}` + "\n"
	if log := logOf(t, home, "k"); log != line {
		t.Errorf("log = %q, want %q", log, line)
	}
}

// Each Finish appends a line, keeping those before it.
func TestTimingsAppend(t *testing.T) {
	home := poolDir(t, "k")
	for _, op := range []string{"open", "close"} {
		tm := Start(ticking(), op)
		tm.Pool(home, "k")
		if _, err := tm.Finish(true); err != nil {
			t.Fatal(err)
		}
	}
	lines := strings.Split(strings.TrimSuffix(logOf(t, home, "k"), "\n"), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], `"operation":"open"`) ||
		!strings.Contains(lines[1], `"operation":"close"`) {
		t.Errorf("log lines = %q, want open's, then close's", lines)
	}
}

// An operation that never found its pool has no timings and logs nothing, and a nil Timer runs
// every step and times none.
func TestTimingsWithoutAPool(t *testing.T) {
	tm := Start(ticking(), "take")
	tm.Slot("slot-1")
	if got, err := tm.Finish(false); got != nil || err != nil {
		t.Errorf("Finish = %+v, %v; want nil, nil", got, err)
	}

	var none *Timer
	ran := false
	if err := none.Step("handout", "", func() error { ran = true; return nil }); err != nil ||
		!ran {
		t.Errorf("nil Step: err %v, ran %t; want nil and fn run", err, ran)
	}
	none.Pool(t.TempDir(), "k")
	none.Slot("slot-1")
	none.Since("clone", "slot-2", start)
	if got, err := none.Finish(true); got != nil || err != nil {
		t.Errorf("nil Finish = %+v, %v; want nil, nil", got, err)
	}
}

// The log goes beside a pool's state file, and an operation whose pool's directory is gone, as
// after `upool deinit`, makes none: Finish returns the timings and the log's error.
func TestTimingsLogNeedsThePoolsDirectory(t *testing.T) {
	home := t.TempDir()
	tm := Start(ticking(), "release")
	tm.Pool(home, "gone-00000000")
	got, err := tm.Finish(true)
	if got == nil || !errors.Is(err, fs.ErrNotExist) ||
		!strings.Contains(err.Error(), "~/.upool/pools/gone-00000000/"+LogFile) {
		t.Errorf("Finish = %+v, %v; want the timings and the missing log's error", got, err)
	}
	if _, err := os.Stat(filepath.Join(home, ".upool")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("~/.upool: %v, want it not made", err)
	}
}
