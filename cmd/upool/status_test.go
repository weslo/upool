package main

import (
	"errors"
	"flag"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/weslo/upool/internal/output"
	"github.com/weslo/upool/internal/pathvars"
	"github.com/weslo/upool/internal/pool"
	"github.com/weslo/upool/internal/registry"
)

// The tests of `upool status`. Like which's, they write registry.json and state.json directly, as
// init and take would leave them, so they need no git and run everywhere. Its output is checked
// against testdata/golden/status-*, which `go test ./cmd/upool -update` rewrites.

var update = flag.Bool("update", false, "rewrite the testdata/golden files this package checks")

// checkGolden compares got, with home scrubbed to ${home}, against testdata/golden/<name>, after
// rewriting the file first under -update.
func checkGolden(t *testing.T, name, home, got string) {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "golden", name)
	got, err := pathvars.Scrub(got, pathvars.Var{Name: "home", Path: home})
	if err != nil {
		t.Fatal(err)
	}
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("output =\n%s\nwant %s:\n%s", got, path, want)
	}
}

// statusHome is a HOME with two registered pools, as status finds them.
type statusHome struct {
	home string
	// down is a pool with a slot in each of the seven states of docs/contracts.md (f); castle is a
	// second pool, with one ready slot and one leased.
	down, castle string
}

// newStatusHome registers two pools under a new HOME, with their main checkouts under
// ~/Documents, where Unity projects live, and their slots under each one's .claude/worktrees.
// Every slot is a directory with an Assets subdirectory. The leases were taken 1h5m, 45s, 2d3h and
// 3m20s before the test clock.
func newStatusHome(t *testing.T) statusHome {
	t.Helper()
	home := tempDir(t)
	h := statusHome{
		home:   home,
		down:   filepath.Join(home, "Documents", "down"),
		castle: filepath.Join(home, "Documents", "castle"),
	}
	slot := func(repo, name string) string {
		return filepath.Join(repo, ".claude", "worktrees", name)
	}
	lease := func(repo, name, holder string, ago time.Duration, confirmed bool) *pool.Lease {
		return &pool.Lease{LeaseID: "lease-" + name, Holder: holder,
			WorktreePath: slot(repo, name), AcquiredAt: clock.Add(-ago), Confirmed: confirmed,
			Lane: "script"}
	}
	owner, ownerStart := 4100, clock.Add(-time.Minute)
	lastUsed := clock.Add(-24 * time.Hour)
	pools := []struct {
		repo, base string
		slots      []pool.Slot
	}{
		{h.down, "main", []pool.Slot{
			{Name: "slot-1", State: pool.StateLeased,
				Lease: lease(h.down, "slot-1", "issue-51", time.Hour+5*time.Minute, true)},
			{Name: "slot-2", State: pool.StateReady},
			{Name: "slot-3", State: pool.StateLeasedUnconfirmed,
				Lease: lease(h.down, "slot-3", "session-3", 45*time.Second, false)},
			{Name: "slot-4", State: pool.StateParked},
			{Name: "slot-5", State: pool.StateNeedsReset},
			{Name: "slot-6", State: pool.StateCreating, OwnerPID: &owner,
				OwnerPIDStart: &ownerStart},
			{Name: "slot-7", State: pool.StateWarming, OwnerPID: &owner,
				OwnerPIDStart: &ownerStart},
			{Name: "slot-10", State: pool.StateLeased,
				Lease: lease(h.down, "slot-10", "run-4242", 51*time.Hour, true)},
		}},
		{h.castle, "trunk", []pool.Slot{
			{Name: "slot-1", State: pool.StateReady},
			{Name: "slot-2", State: pool.StateLeased,
				Lease: lease(h.castle, "slot-2", "issue-7", 3*time.Minute+20*time.Second, true)},
		}},
	}
	for _, p := range pools {
		for i := range p.slots {
			s := &p.slots[i]
			s.Path, s.LastUsed = slot(p.repo, s.Name), lastUsed
			if err := os.MkdirAll(filepath.Join(s.Path, "Assets"), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		reg, err := registry.Add(t.Context(), home, p.repo)
		if err != nil {
			t.Fatal(err)
		}
		err = pool.Update(t.Context(), home, reg.Key, func(state *pool.Pool) error {
			state.Repo, state.Base, state.Slots = p.repo, p.base, p.slots
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return h
}

// runStatus runs `upool status args…` in HOME home with wd as the working directory.
func runStatus(t *testing.T, home, wd string, args ...string) result {
	t.Helper()
	env := map[string]string{"HOME": home, "PWD": wd}
	return dispatch(t, commandTable, hookTable, env, "", append([]string{"status"}, args...)...)
}

// checkSucceeded fails the test unless r exited 0 with nothing on stderr.
func checkSucceeded(t *testing.T, r result) {
	t.Helper()
	if r.code != output.ExitOK || r.stderr != "" {
		t.Errorf("exit = %d with stderr %q, want %d and empty", r.code, r.stderr, output.ExitOK)
	}
}

// From the main checkout, anywhere under it, or inside one of its slots, status shows that pool
// alone, and the other registered pool not at all.
func TestStatusShowsThePoolContainingTheWorkingDirectory(t *testing.T) {
	h := newStatusHome(t)
	worktrees := filepath.Join(h.down, ".claude", "worktrees")
	for _, c := range []struct {
		name, wd string
	}{
		{"the main checkout", h.down},
		{"a subdirectory of the main checkout", filepath.Join(h.down, "Assets")},
		{"the worktrees directory", worktrees},
		{"a slot", filepath.Join(worktrees, "slot-1")},
		{"a subdirectory of a slot", filepath.Join(worktrees, "slot-10", "Assets")},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := runStatus(t, h.home, c.wd)
			checkSucceeded(t, r)
			checkGolden(t, "status-one-pool.stdout", h.home, r.stdout)
			if strings.Contains(r.stdout, h.castle) {
				t.Errorf("stdout = %q, want no mention of the other pool, %s", r.stdout, h.castle)
			}

			r = runStatus(t, h.home, c.wd, "--json")
			checkSucceeded(t, r)
			checkGolden(t, "status-one-pool.json", h.home, r.stdout)
		})
	}

	r := runStatus(t, h.home, filepath.Join(h.castle, ".claude", "worktrees", "slot-2"))
	checkSucceeded(t, r)
	if want := h.castle + " (base trunk)\n"; !strings.HasPrefix(r.stdout, want) ||
		strings.Contains(r.stdout, h.down) {
		t.Errorf("stdout from castle's slot = %q, want castle's pool alone, starting %q",
			r.stdout, want)
	}
}

// A pool registered inside another's main checkout answers for its own directory; the outer pool
// answers everywhere else under its checkout.
func TestStatusShowsTheInnermostPool(t *testing.T) {
	h := newStatusHome(t)
	inner := filepath.Join(h.down, "Packages", "inner")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	reg, err := registry.Add(t.Context(), h.home, inner)
	if err != nil {
		t.Fatal(err)
	}
	err = pool.Update(t.Context(), h.home, reg.Key, func(p *pool.Pool) error {
		p.Repo, p.Base = inner, "main"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ wd, want string }{
		{inner, inner},
		{filepath.Join(h.down, "Packages"), h.down},
	} {
		r := runStatus(t, h.home, c.wd)
		checkSucceeded(t, r)
		if want := c.want + " (base main)\n"; !strings.HasPrefix(r.stdout, want) ||
			strings.Count(r.stdout, "(base ") != 1 {
			t.Errorf("stdout from %s = %q, want one pool, starting %q", c.wd, r.stdout, want)
		}
	}
}

// --all shows every registered pool, in the order they were registered, wherever it runs.
func TestStatusAllShowsEveryPool(t *testing.T) {
	h := newStatusHome(t)
	for _, wd := range []string{tempDir(t), h.castle, h.down} {
		r := runStatus(t, h.home, wd, "--all")
		checkSucceeded(t, r)
		checkGolden(t, "status-all.stdout", h.home, r.stdout)

		r = runStatus(t, h.home, wd, "--all", "--json")
		checkSucceeded(t, r)
		checkGolden(t, "status-all.json", h.home, r.stdout)
	}
}

func TestStatusAllWithNothingRegisteredSaysSo(t *testing.T) {
	home := tempDir(t)
	r := runStatus(t, home, tempDir(t), "--all")
	checkSucceeded(t, r)
	if want := "no pools are registered\n"; r.stdout != want {
		t.Errorf("stdout = %q, want %q", r.stdout, want)
	}

	r = runStatus(t, home, tempDir(t), "--all", "--json")
	checkSucceeded(t, r)
	env := decode(t, r.stdout)
	if got, want := mustJSON(t, env.Data), `{"pools":[]}`; !env.Success || string(got) != want {
		t.Errorf("envelope = %+v with data %s, want success with %s", env, got, want)
	}
}

// Without --all, status outside every registered pool fails rather than showing nothing, whether
// other pools are registered or none are.
func TestStatusOutsideEveryPoolFails(t *testing.T) {
	h := newStatusHome(t)
	outside := tempDir(t)
	for _, c := range []struct {
		name, home, wd string
	}{
		{"pools registered elsewhere", h.home, outside},
		{"the directory holding the pools", h.home, filepath.Dir(h.down)},
		{"nothing registered", tempDir(t), h.down},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := runStatus(t, c.home, c.wd)
			if r.code != output.ExitFailure || r.stdout != "" {
				t.Errorf("exit = %d with stdout %q, want %d and empty", r.code, r.stdout,
					output.ExitFailure)
			}
			want := "upool: " + c.wd + " is not inside a registered pool; run upool status " +
				"--all to see every pool\n"
			if r.stderr != want {
				t.Errorf("stderr = %q, want %q", r.stderr, want)
			}

			r = runStatus(t, c.home, c.wd, "--json")
			env := decode(t, r.stdout)
			if r.code != output.ExitFailure || env.Success || env.Command != "status" ||
				env.Data != nil || len(env.Errors) != 1 ||
				env.Errors[0].Code != string(output.CodeCommandFailed) {
				t.Errorf("--json: exit = %d, envelope = %+v, want %d and a failure for status "+
					"with no data and one %s error", r.code, env, output.ExitFailure,
					output.CodeCommandFailed)
			}
		})
	}
}

// status has no side effects: it reads the state under the lock and writes none of it, and makes
// no ~/.upool where there is none.
func TestStatusWritesNothing(t *testing.T) {
	h := newStatusHome(t)
	state := filepath.Join(h.home, ".upool")
	before := snapshot(t, state)
	for _, args := range [][]string{nil, {"--json"}, {"--all"}, {"--all", "--json"}} {
		runStatus(t, h.home, h.down, args...)
	}
	after := snapshot(t, state)
	if string(mustJSON(t, after)) != string(mustJSON(t, before)) {
		t.Errorf("~/.upool after status = %v, want it unchanged: %v", after, before)
	}

	empty := tempDir(t)
	for _, args := range [][]string{nil, {"--all"}} {
		runStatus(t, empty, h.down, args...)
	}
	if _, err := os.Stat(filepath.Join(empty, ".upool")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("~/.upool after status with nothing registered: stat error = %v, want it not "+
			"to exist", err)
	}
}

// status takes no arguments; it does not quietly answer for the working directory when given a
// path.
func TestStatusRefusesArguments(t *testing.T) {
	h := newStatusHome(t)
	r := runStatus(t, h.home, h.down, h.castle)
	if r.code != output.ExitFailure || r.stdout != "" {
		t.Errorf("exit = %d with stdout %q, want %d and empty", r.code, r.stdout,
			output.ExitFailure)
	}
	if want := "unexpected arguments"; !strings.Contains(r.stderr, want) ||
		!strings.Contains(r.stderr, "usage: upool "+statusUsage) {
		t.Errorf("stderr = %q, want %q and the usage", r.stderr, want)
	}
}

func TestStatusLeaseAgeShowsItsTwoLargestUnits(t *testing.T) {
	for _, c := range []struct {
		d    time.Duration
		want string
	}{
		{-time.Minute, "0s"},
		{0, "0s"},
		{999 * time.Millisecond, "0s"},
		{45 * time.Second, "45s"},
		{time.Minute, "1m"},
		{90 * time.Second, "1m30s"},
		{time.Hour, "1h"},
		{time.Hour + 5*time.Minute + 30*time.Second, "1h5m"},
		{24 * time.Hour, "1d"},
		{51*time.Hour + 59*time.Minute, "2d3h"},
		{400 * 24 * time.Hour, "400d"},
	} {
		if got := leaseAge(c.d); got != c.want {
			t.Errorf("leaseAge(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}
