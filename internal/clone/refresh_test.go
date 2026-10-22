package clone

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/weslo/upool/internal/ledger"
	"github.com/weslo/upool/internal/pathvars"
	"github.com/weslo/upool/internal/pool"
	"github.com/weslo/upool/internal/proc"
	"github.com/weslo/upool/internal/testenv"
	"github.com/weslo/upool/internal/unity"
	"github.com/weslo/upool/internal/unity/fake"
)

// The tests of the stale-seed refresh ((g) step 1; #98), with the fake unity replaying the
// captures of a headless `unity open`, `unity status --until-ready` and `unity close`, and the
// process table injected, since the Editor's PID is the capture's.

// launchedPID is launchedPid in testdata/captures/unity/open-headless.json.
const launchedPID = 25147

// The fake's replies for a refresh of the checkout at repo: open, a ready status, and close.
func openHeadless(repo string) fake.Reply {
	return fake.Reply{Capture: "open-headless.json", Vars: []pathvars.Var{{Name: "repo",
		Path: repo}}}
}

var (
	statusReady       = fake.Reply{Capture: "status-until-ready-ready.json", Vars: spike}
	statusNoInstances = fake.Reply{Capture: "status-until-ready-no-instances.json", Exit: 6}
	closeHeadless     = fake.Reply{Capture: "close-headless.json", Vars: spike}
	spike             = []pathvars.Var{{Name: "spike", Path: "/spike"}}
)

// refreshProcs is the process table of a refresh: the cloning process alive throughout, and the
// Editor it launches, launchedPID, running for its first starts Start checks, then gone, as once
// `unity close` has returned; with exited, its process has exited as soon as Alive looks. during,
// when not nil, runs at the Editor's first Start check, while it starts.
type refreshProcs struct {
	starts atomic.Int32
	exited bool
	during func()
	fired  atomic.Bool
}

func newRefreshProcs(starts int, during func()) *refreshProcs {
	p := &refreshProcs{during: during}
	p.starts.Store(int32(starts))
	return p
}

func (p *refreshProcs) Start(pid int) (time.Time, error) {
	switch pid {
	case clonerPID:
		return procStart, nil
	case launchedPID:
		if p.during != nil && p.fired.CompareAndSwap(false, true) {
			p.during()
		}
		if p.starts.Add(-1) < 0 {
			return time.Time{}, proc.ErrNoProcess
		}
		return procStart, nil
	}
	return time.Time{}, proc.ErrNoProcess
}

func (p *refreshProcs) Alive(pid int, start time.Time) (bool, error) {
	switch pid {
	case clonerPID:
		return true, nil
	case launchedPID:
		return !p.exited && p.starts.Load() >= 0, nil
	}
	return false, nil
}

// newRefresher is a SeedRefresher under e's home, as clonerPID, with the fake u alone on its PATH
// and procs as the process table.
func newRefresher(e *testenv.Env, u *fake.Unity, procs proc.Checker) *SeedRefresher {
	return &SeedRefresher{
		Unity:    unity.Client{Runner: unity.Exec{}, Env: keyEnv(u)},
		Proc:     procs,
		Budget:   ledger.Budget{RAMGB: 16, EditorGB: 2.5, HelperGB: 1, MaxColdImports: 1},
		Home:     e.Home,
		OwnerPID: clonerPID,
		Now:      func() time.Time { return procStart },
	}
}

// compile makes the Library at dir current, as an Editor does once it has compiled: its
// ScriptAssemblies built after every script testenv writes.
func compile(t *testing.T, dir string) {
	t.Helper()
	warmLibrary(t, dir, testenv.StaleTime.Add(time.Hour))
}

// editors returns the Editors the ledger under home records.
func editors(t *testing.T, home string) []ledger.Editor {
	t.Helper()
	l, err := ledger.Load(home)
	if err != nil {
		t.Fatal(err)
	}
	return l.Editors
}

// refreshCalls are the calls of a refresh of repo: open headless, the wait for 120 s (A37) and,
// with closed, the close, each with repo positional or targeted.
func refreshCalls(repo string, closed bool) [][]string {
	calls := [][]string{{"open", repo, "--args", "-batchmode", "--format", "json"},
		{"status", "--until-ready", "--timeout", "120", "--format", "json"}}
	if closed {
		calls = append(calls, []string{"close", repo, "--format", "json"})
	}
	return calls
}

// checkCalls fails the test unless u's calls are want, each targeting dir.
func checkCalls(t *testing.T, u *fake.Unity, dir string, want [][]string) {
	t.Helper()
	calls := u.Calls()
	var got [][]string
	for _, c := range calls {
		got = append(got, c.Args)
		if c.ProjectPath != dir {
			t.Errorf("unity %v with UNITY_PROJECT_PATH %q, want %s", c.Args, c.ProjectPath, dir)
		}
	}
	if !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("unity calls = %q, want %q", got, want)
	}
}

// A refresh opens the seed's Editor headless, charged in the ledger under the Editor's PID while
// it runs, waits until it is ready, closes it without save_all and takes it out of the ledger. The
// scene list the headless Editor empties is put back (A8).
func TestSeedRefresherOpensWaitsAndCloses(t *testing.T) {
	e := testenv.New(t, testenv.StaleLibrary())
	scenes := e.Path("Library/" + pool.SceneListFile)
	e.WriteFile("Library/"+pool.SceneListFile, "sceneSetups: [Main]\n")
	u := fake.Install(t, openHeadless(e.Repo), statusReady, closeHeadless)
	var charged []ledger.Editor
	procs := newRefreshProcs(1, func() {
		charged = editors(t, e.Home)
		if err := os.WriteFile(scenes, []byte("sceneSetups: []\n"), 0o644); err != nil {
			t.Error(err)
		}
	})
	var said []string
	r := newRefresher(e, u, procs)
	r.Say = func(line string) { said = append(said, line) }
	warnings, err := r.Refresh(t.Context(), e.Repo)
	if err != nil {
		t.Fatal(err)
	}
	checkCalls(t, u, e.Repo, refreshCalls(e.Repo, true))
	want := []ledger.Editor{{Project: e.Repo, PID: launchedPID, Mode: "headless",
		OpenedByUpool: true, ChargeGB: 2.5}}
	if !slices.Equal(charged, want) {
		t.Errorf("ledger while the Editor started = %+v, want %+v", charged, want)
	}
	if got := editors(t, e.Home); len(got) != 0 {
		t.Errorf("ledger afterwards = %+v, want empty", got)
	}
	if got, _ := os.ReadFile(scenes); string(got) != "sceneSetups: [Main]\n" {
		t.Errorf("%s = %q, want it put back", pool.SceneListFile, got)
	}
	if len(said) != 1 || !strings.Contains(said[0], "is stale; refreshing it with a headless "+
		"Editor first") {
		t.Errorf("said %q, want the refresh announced", said)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "Hub project registry") {
		t.Errorf("warnings = %q, want the CLI's Hub-registry warning", warnings)
	}
}

// A refresh that does not end with a ready Editor fails. An Editor still running when the wait
// runs out is closed without save_all, as A40 does for a re-warm; if that close fails, the Editor
// stays in the ledger, since it may still be up. An Editor that exited is withdrawn.
func TestSeedRefresherFailures(t *testing.T) {
	notReady := fake.Reply{Capture: "status-until-ready-no-instances.json", Exit: 6}
	for _, tt := range []struct {
		name    string
		replies []fake.Reply
		exited  bool
		closed  bool
		// kept is whether the Editor stays in the ledger.
		kept bool
	}{
		{"not ready, closed", []fake.Reply{notReady, closeHeadless}, false, true, false},
		{"not ready, the close failed", []fake.Reply{notReady, statusNoInstances}, false, true,
			true},
		{"exited before ready", []fake.Reply{notReady}, true, false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := testenv.New(t, testenv.StaleLibrary())
			u := fake.Install(t, append([]fake.Reply{openHeadless(e.Repo)}, tt.replies...)...)
			procs := newRefreshProcs(1, nil)
			procs.exited = tt.exited
			_, err := newRefresher(e, u, procs).Refresh(t.Context(), e.Repo)
			var notReady *unity.NotReadyError
			switch {
			case err == nil:
				t.Fatal("Refresh succeeded, want it to fail")
			case tt.exited && !strings.Contains(err.Error(), "exited before it was ready"):
				t.Errorf("err = %v, want the Editor's exit", err)
			case !tt.exited && !errors.As(err, &notReady):
				t.Errorf("err = %v, want a *unity.NotReadyError", err)
			}
			checkCalls(t, u, e.Repo, refreshCalls(e.Repo, tt.closed))
			got := editors(t, e.Home)
			if tt.kept != (len(got) == 1) || len(got) > 1 {
				t.Errorf("ledger = %+v, want the Editor kept: %t", got, tt.kept)
			}
		})
	}
}

// A refused admission launches nothing.
func TestSeedRefresherRefusedLaunchesNothing(t *testing.T) {
	e := testenv.New(t, testenv.StaleLibrary())
	u := fake.Install(t)
	r := newRefresher(e, u, newRefreshProcs(1, nil))
	r.Budget.RAMGB = 1
	if _, err := r.Refresh(t.Context(), e.Repo); err == nil {
		t.Fatal("Refresh succeeded, want the budget to refuse it")
	}
	checkCalls(t, u, e.Repo, nil)
	if got := editors(t, e.Home); len(got) != 0 {
		t.Errorf("ledger = %+v, want empty", got)
	}
}

// The seed choice refreshes a stale candidate while it holds it, and chooses it once it is
// current; a candidate whose refresh fails, or that its refresh leaves dirty, is passed over for
// the next, the main checkout here. A slot passed over is given back ready, unless its refresh
// left it dirty: it stays warming, for transition 22 to park once the cloner has exited.
func TestChooseSeedRefreshesAStaleCandidate(t *testing.T) {
	stale := testenv.ScriptTime.Add(-time.Hour)
	for _, tt := range []struct {
		name string
		// unity is whether the fake unity answers; without it the launch fails.
		unity bool
		// during runs while slot-1's Editor starts.
		during func(t *testing.T, slot string)
		seed   string
		state  pool.State
	}{
		{"refreshed and chosen", true, func(t *testing.T, slot string) { compile(t, slot) },
			"slot-1", pool.StateWarming},
		{"the refresh failed", false, nil, "", pool.StateReady},
		{"the refresh left it dirty", true, func(t *testing.T, slot string) {
			compile(t, slot)
			if err := os.WriteFile(filepath.Join(slot, "left.txt"), []byte("x\n"),
				0o644); err != nil {
				t.Error(err)
			}
		}, "", pool.StateWarming},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := newSeedEnv(t, []pool.State{pool.StateReady}, []time.Time{stale})
			slot := e.slot(t, "slot-1").Path
			var u *fake.Unity
			if tt.unity {
				u = fake.Install(t, openHeadless(slot), statusReady, closeHeadless)
			} else {
				u = fake.Install(t)
				if err := os.Remove(filepath.Join(u.Dir(), "unity")); err != nil {
					t.Fatal(err)
				}
			}
			var during func()
			if tt.during != nil {
				during = func() { tt.during(t, slot) }
			}
			e.sd.Refresher = newRefresher(e.Env, u, newRefreshProcs(1, during))
			seed, why, err := e.sd.Choose(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			want := Seed{Path: e.Repo}
			if tt.seed != "" {
				want = Seed{Path: slot, Slot: tt.seed}
			}
			if seed != want {
				t.Errorf("seed = %+v, want %+v; passed over %q", seed, want, why)
			}
			if got := e.slot(t, "slot-1").State; got != tt.state {
				t.Errorf("slot-1 = %s, want %s", got, tt.state)
			}
			if tt.seed == "" && (len(why) != 1 || !strings.HasPrefix(why[0], "slot-1: ")) {
				t.Errorf("passed over %q, want slot-1 with its reason", why)
			}
		})
	}
}
