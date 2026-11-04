package main

import (
	"errors"
	"flag"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/weslo/upool/internal/config"
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
	// slot-7's owner is alive, so that status, which checks a warming slot again once its owner
	// has died (#97), leaves it warming.
	warmer, warmerStart := liveOwner(t)
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
			{Name: "slot-7", State: pool.StateWarming, OwnerPID: &warmer,
				OwnerPIDStart: &warmerStart},
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

// runStatus runs `upool status args…` in HOME home with wd as the working directory. It sets
// editor_ram_budget_gb, as newInitEnv does, because status loads a pool's configuration to expire
// its unconfirmed leases (#75), and off macOS that setting's default, a quarter of physical RAM,
// cannot be read.
func runStatus(t *testing.T, home, wd string, args ...string) result {
	t.Helper()
	env := map[string]string{"HOME": home, "PWD": wd,
		config.EnvName("editor_ram_budget_gb"): "16"}
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
// alone, and the other registered pool not at all. Inside a slot it marks that slot as the one
// the working directory is in, and outside every slot it marks none (#283).
func TestStatusShowsThePoolContainingTheWorkingDirectory(t *testing.T) {
	h := newStatusHome(t)
	worktrees := filepath.Join(h.down, ".claude", "worktrees")
	for _, c := range []struct {
		name, wd, golden string
	}{
		{"the main checkout", h.down, "status-one-pool"},
		{"a subdirectory of the main checkout", filepath.Join(h.down, "Assets"), "status-one-pool"},
		{"the worktrees directory", worktrees, "status-one-pool"},
		{"a slot", filepath.Join(worktrees, "slot-1"), "status-here"},
		{"a subdirectory of a slot", filepath.Join(worktrees, "slot-1", "Assets"), "status-here"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := runStatus(t, h.home, c.wd)
			checkSucceeded(t, r)
			checkGolden(t, c.golden+".stdout", h.home, r.stdout)
			if strings.Contains(r.stdout, h.castle) {
				t.Errorf("stdout = %q, want no mention of the other pool, %s", r.stdout, h.castle)
			}

			r = runStatus(t, h.home, c.wd, "--json")
			checkSucceeded(t, r)
			checkGolden(t, c.golden+".json", h.home, r.stdout)
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

// status marks the one slot the working directory is in, as which finds it, by its path: not
// slot-1 from inside slot-10, and with --all not down's slot-2 from inside castle's. Outside every
// slot it marks none (#283).
func TestStatusMarksOnlyTheSlotTheWorkingDirectoryIsIn(t *testing.T) {
	h := newStatusHome(t)
	slot := func(repo, name string) string {
		return filepath.Join(repo, ".claude", "worktrees", name)
	}
	for _, c := range []struct {
		name string
		wd   string
		args []string
		want []string
	}{
		{"a subdirectory of slot-10", filepath.Join(slot(h.down, "slot-10"), "Assets"), nil,
			[]string{"down/slot-10"}},
		{"an unconfirmed lease's slot", slot(h.down, "slot-3"), nil, []string{"down/slot-3"}},
		{"a creating slot", slot(h.down, "slot-6"), nil, []string{"down/slot-6"}},
		{"castle's slot-2, with --all", slot(h.castle, "slot-2"), []string{"--all"},
			[]string{"castle/slot-2"}},
		{"down's slot-1, with --all", slot(h.down, "slot-1"), []string{"--all"},
			[]string{"down/slot-1"}},
		{"the main checkout, with --all", h.down, []string{"--all"}, nil},
		{"outside every pool, with --all", tempDir(t), []string{"--all"}, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := runStatus(t, h.home, c.wd, c.args...)
			checkSucceeded(t, r)
			if got := markedRows(r.stdout); !slices.Equal(got, c.want) {
				t.Errorf("rows marked (here) = %q, want %q; stdout:\n%s", got, c.want, r.stdout)
			}

			r = runStatus(t, h.home, c.wd, append(c.args, "--json")...)
			checkSucceeded(t, r)
			if got := markedSlots(t, r.stdout); !slices.Equal(got, c.want) {
				t.Errorf("--json: slots with here = %q, want %q", got, c.want)
			}
		})
	}
}

// markedRows names each row of status's table that is marked "(here)", as the base name of its
// pool's main checkout, a "/" and the slot's name.
func markedRows(stdout string) []string {
	var marked []string
	for section := range strings.SplitSeq(strings.TrimSuffix(stdout, "\n"), "\n\n") {
		lines := strings.Split(section, "\n")
		repo, _, _ := strings.Cut(lines[0], " (base ")
		for _, line := range lines[1:] {
			if name, _, ok := strings.Cut(line, " (here) "); ok && !strings.Contains(name, " ") {
				marked = append(marked, filepath.Base(repo)+"/"+name)
			}
		}
	}
	return marked
}

// markedSlots names each slot status --json marks here, as markedRows names it.
func markedSlots(t *testing.T, stdout string) []string {
	t.Helper()
	var data struct {
		Pools []struct {
			Repo  string `json:"repo"`
			Slots []struct {
				Name string `json:"name"`
				Here bool   `json:"here"`
			} `json:"slots"`
		} `json:"pools"`
	}
	decodeData(t, stdout, &data)
	var marked []string
	for _, p := range data.Pools {
		for _, s := range p.Slots {
			if s.Here {
				marked = append(marked, filepath.Base(p.Repo)+"/"+s.Name)
			}
		}
	}
	return marked
}

// Without --all, status shows its pool as --all shows it from the same directory: every slot in
// whatever state, the creating slot-6 and warming slot-7 among them, and the refresh job, from the
// main checkout and from inside a slot, where a session runs it (#283).
func TestStatusShowsItsPoolAsAllDoes(t *testing.T) {
	checkShowsItsPoolAsAllDoes(t, newStatusHome(t))
}

// checkShowsItsPoolAsAllDoes fails the test unless status, from down's main checkout and from
// inside two of its slots, shows down's pool, its creating and warming slots included, as status
// --all does from the same directory, in both forms.
func checkShowsItsPoolAsAllDoes(t *testing.T, h statusHome) {
	t.Helper()
	worktrees := filepath.Join(h.down, ".claude", "worktrees")
	for _, wd := range []string{h.down, filepath.Join(worktrees, "slot-1"),
		filepath.Join(worktrees, "slot-3", "Assets")} {
		plain, all := runStatus(t, h.home, wd), runStatus(t, h.home, wd, "--all")
		checkSucceeded(t, plain)
		checkSucceeded(t, all)
		// down was registered first, so --all shows it first.
		if !strings.HasPrefix(all.stdout, plain.stdout) {
			t.Errorf("from %s: stdout =\n%s\nwant down's part of --all's:\n%s", wd, plain.stdout,
				all.stdout)
		}

		plain, all = runStatus(t, h.home, wd, "--json"), runStatus(t, h.home, wd, "--all", "--json")
		var mine, every struct {
			Pools []map[string]any `json:"pools"`
		}
		decodeData(t, plain.stdout, &mine)
		decodeData(t, all.stdout, &every)
		if len(mine.Pools) != 1 || len(every.Pools) != 2 {
			t.Fatalf("from %s: %d pools, and %d with --all; want 1 and 2", wd, len(mine.Pools),
				len(every.Pools))
		}
		if got, want := mustJSON(t, mine.Pools[0]), mustJSON(t, every.Pools[0]); string(got) !=
			string(want) {
			t.Errorf("from %s: --json pool = %s, want --all's for down, %s", wd, got, want)
		}
		var slots struct {
			Pools []struct {
				Slots []struct {
					Name  string     `json:"name"`
					State pool.State `json:"state"`
				} `json:"slots"`
			} `json:"pools"`
		}
		decodeData(t, plain.stdout, &slots)
		states := map[string]pool.State{}
		for _, s := range slots.Pools[0].Slots {
			states[s.Name] = s.State
		}
		if states["slot-6"] != pool.StateCreating || states["slot-7"] != pool.StateWarming {
			t.Errorf("from %s: slot-6 is %q and slot-7 %q, want %s and %s", wd, states["slot-6"],
				states["slot-7"], pool.StateCreating, pool.StateWarming)
		}
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

// --all shows every registered pool, in the order they were registered, wherever it runs, even
// where the working directory, which then only marks a slot (#283), cannot be read: an empty PWD,
// which has no canonical key.
func TestStatusAllShowsEveryPool(t *testing.T) {
	h := newStatusHome(t)
	for _, wd := range []string{tempDir(t), h.castle, h.down, ""} {
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
