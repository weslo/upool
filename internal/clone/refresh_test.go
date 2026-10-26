package clone

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
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
	// closeUnreachable and closeForced are the replies of `unity close` and `unity close --force`
	// to an Editor in the middle of a cold import, which the first leaves running (#224), and
	// noInstances that of the `unity status` after a forced close, listing no Editor (A45).
	closeUnreachable = fake.Reply{Capture: "close-unreachable.json", Vars: repoVar, Exit: 6}
	closeForced      = fake.Reply{Capture: "close-force-unreachable.json", Vars: repoVar}
	noInstances      = fake.Reply{Capture: "status-no-instances.json", Exit: 6}
	repoVar          = []pathvars.Var{{Name: "repo", Path: "/repo"}}
	// closeTimedOut is the reply of a plain `unity close` sent as soon as a headless Editor of a
	// main checkout was ready, which timed out and left the Editor running (#252).
	closeTimedOut = fake.Reply{Capture: "close-timeout.json", Vars: repoVar, Exit: 6}
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

// Name names the launched Editor a Unity Editor while it runs, and the cloning process upool.
func (p *refreshProcs) Name(pid int) (string, error) {
	switch {
	case pid == clonerPID:
		return "upool.test", nil
	case pid == launchedPID && !p.exited && p.starts.Load() >= 0:
		return unityProcess, nil
	}
	return "", proc.ErrNoProcess
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

// How a refresh's Editor is closed: not at all, with a plain `unity close`, as a ready one is, or
// with `unity close --force`, as one that is not ready is (A40; #224), and then `unity status`,
// before its instance files go (A45), or not, when the forced close failed.
type closing int

const (
	notClosed closing = iota
	closedPlain
	closedForced
	forcedCloseFailed
)

// refreshCalls are the calls of a refresh of repo: open headless, the wait for 120 s (A37), and
// the close as closed says, each with repo positional or targeted.
func refreshCalls(repo string, closed closing) [][]string {
	return refreshCallsWaiting(repo, "120", closed)
}

// refreshCallsWaiting are refreshCalls with a wait of timeout seconds.
func refreshCallsWaiting(repo, timeout string, closed closing) [][]string {
	calls := [][]string{{"open", repo, "--args", "-batchmode", "--format", "json"},
		{"status", "--until-ready", "--timeout", timeout, "--format", "json"}}
	switch closed {
	case closedPlain:
		calls = append(calls, []string{"close", repo, "--format", "json"})
	case closedForced:
		calls = append(calls, []string{"close", repo, "--force", "--format", "json"},
			[]string{"status", "--format", "json"})
	case forcedCloseFailed:
		calls = append(calls, []string{"close", repo, "--force", "--format", "json"})
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
	warnings, err := r.Refresh(t.Context(), e.Repo, false)
	if err != nil {
		t.Fatal(err)
	}
	checkCalls(t, u, e.Repo, refreshCalls(e.Repo, closedPlain))
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
// runs out is closed without save_all and with --force, as A40 does for a re-warm, since plain
// `unity close` cannot reach an Editor in the middle of a cold import (#224), and `unity status`
// is asked whether any Editor is left before its instance files go (A45); if that close fails,
// the Editor stays in the ledger, since it may still be up. An Editor that exited is withdrawn.
func TestSeedRefresherFailures(t *testing.T) {
	notReady := fake.Reply{Capture: "status-until-ready-no-instances.json", Exit: 6}
	for _, tt := range []struct {
		name    string
		replies []fake.Reply
		exited  bool
		closed  closing
		// kept is whether the Editor stays in the ledger.
		kept bool
	}{
		{"not ready, closed", []fake.Reply{notReady, closeForced, noInstances}, false,
			closedForced, false},
		{"not ready, the close failed", []fake.Reply{notReady, closeUnreachable}, false,
			forcedCloseFailed, true},
		{"exited before ready", []fake.Reply{notReady}, true, notClosed, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := testenv.New(t, testenv.StaleLibrary())
			u := fake.Install(t, append([]fake.Reply{openHeadless(e.Repo)}, tt.replies...)...)
			procs := newRefreshProcs(1, nil)
			procs.exited = tt.exited
			_, err := newRefresher(e, u, procs).Refresh(t.Context(), e.Repo, false)
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

// After a forced close, which terminates the Editor, the instance files it left in the seed are
// removed once its process is gone and `unity status` lists no Editor there: only when
// EditorInstance.json names that Editor, and not while another Editor is listed (A45; #243).
func TestSeedRefresherClearsWhatAForcedCloseLeft(t *testing.T) {
	files := []string{testenv.EditorInstanceFile, "Library/ProtocolInstance.json",
		"Temp/UnityLockfile"}
	oneEditor := fake.Reply{Capture: "status-one-editor.json", Vars: spike}
	for _, tt := range []struct {
		name    string
		pid     int
		status  fake.Reply
		cleared bool
		warning string
		// running is whether the Editor's process outlives the wait after the forced close
		// reported it closed, which asks unity status nothing.
		running bool
	}{
		{"its own files", launchedPID, noInstances, true, "", false},
		{"another Editor's files", 4242, noInstances, false, "", false},
		{"an Editor still listed", launchedPID, oneEditor, false, "unity status lists 1 Editor",
			false},
		{"its process still running", launchedPID, fake.Reply{}, false, "", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := testenv.New(t, testenv.StaleLibrary())
			e.WriteFile(testenv.EditorInstanceFile, fmt.Sprintf(`{"process_id": %d}`, tt.pid))
			e.WriteFile("Library/ProtocolInstance.json", "[]\n")
			e.WriteFile("Temp/UnityLockfile", "")
			notReady := fake.Reply{Capture: "status-until-ready-no-instances.json", Exit: 6}
			replies := []fake.Reply{openHeadless(e.Repo), notReady, closeForced}
			procs := newRefreshProcs(1, nil)
			calls := refreshCalls(e.Repo, closedForced)
			if tt.running {
				procs = newRefreshProcs(1000, nil)
				calls = refreshCalls(e.Repo, forcedCloseFailed)
			} else {
				replies = append(replies, tt.status)
			}
			u := fake.Install(t, replies...)
			r := newRefresher(e, u, procs)
			if tt.running {
				// Each reading of the clock is past the last wait's end.
				var ticks atomic.Int64
				r.Now = func() time.Time {
					return procStart.Add(time.Duration(ticks.Add(1)) * unity.ExitTimeout)
				}
			}
			warnings, err := r.Refresh(t.Context(), e.Repo, false)
			var nr *unity.NotReadyError
			if !errors.As(err, &nr) {
				t.Fatalf("Refresh = %v, want a *unity.NotReadyError", err)
			}
			if tt.running && !errors.Is(err, ErrEditorLeft) {
				t.Errorf("Refresh = %v, want the Editor left", err)
			}
			checkCalls(t, u, e.Repo, calls)
			for _, name := range files {
				_, err := os.Stat(e.Path(name))
				if gone := err != nil; gone != tt.cleared {
					t.Errorf("%s: %v; want it gone: %t", name, err, tt.cleared)
				}
			}
			found := slices.ContainsFunc(warnings, func(w string) bool {
				return tt.warning != "" && strings.Contains(w, tt.warning)
			})
			if tt.warning != "" && !found {
				t.Errorf("warnings = %q, want one saying %q", warnings, tt.warning)
			}
		})
	}
}

// A ready Editor that a plain close leaves running, timed out or unreachable, is sent the close
// again, and when that one closes it the refresh ends as after a first close: no --force, the
// ledger empty, the scene list put back (A48; #252). A plain close that fails any other way is
// followed by --force at once, and an Editor whose process has gone by the time a close fails is
// sent nothing more. The wait between closes passes on testing/synctest's clock.
func TestSeedRefresherRetriesAReadyEditorsPlainClose(t *testing.T) {
	plain := func(repo string) []string { return []string{"close", repo, "--format", "json"} }
	otherFailure := closeTimedOut
	otherFailure.Replace = []string{"PROJECTS_CLOSE_TIMEOUT", "PROJECTS_CLOSE_FAILED"}
	for _, tt := range []struct {
		name    string
		replies []fake.Reply
		// starts is how many of the Editor's Start checks find it running.
		starts int
		calls  func(repo string) [][]string
		waited time.Duration
	}{
		{"timed out", []fake.Reply{closeTimedOut, closeHeadless}, 2, func(repo string) [][]string {
			return append(refreshCalls(repo, closedPlain), plain(repo))
		}, closeRetryInterval},
		{"unreachable", []fake.Reply{closeUnreachable, closeHeadless}, 2,
			func(repo string) [][]string {
				return append(refreshCalls(repo, closedPlain), plain(repo))
			}, closeRetryInterval},
		{"failed otherwise", []fake.Reply{otherFailure, closeForced, noInstances}, 2,
			func(repo string) [][]string {
				calls := refreshCalls(repo, closedPlain)
				return append(calls, refreshCalls(repo, closedForced)[2:]...)
			}, 0},
		{"gone meanwhile", []fake.Reply{closeTimedOut}, 1, func(repo string) [][]string {
			return refreshCalls(repo, closedPlain)
		}, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := testenv.New(t, testenv.StaleLibrary())
			scenes := e.Path("Library/" + pool.SceneListFile)
			e.WriteFile("Library/"+pool.SceneListFile, "sceneSetups: [Main]\n")
			u := fake.Install(t, append([]fake.Reply{openHeadless(e.Repo), statusReady},
				tt.replies...)...)
			synctest.Test(t, func(t *testing.T) {
				procs := newRefreshProcs(tt.starts, func() {
					if err := os.WriteFile(scenes, []byte("sceneSetups: []\n"), 0o644); err != nil {
						t.Error(err)
					}
				})
				r := newRefresher(e, u, procs)
				r.Now = time.Now
				begin := time.Now()
				if _, err := r.Refresh(t.Context(), e.Repo, false); err != nil {
					t.Fatalf("Refresh: %v, want no error", err)
				}
				if waited := time.Since(begin); waited != tt.waited {
					t.Errorf("Refresh waited %s, want %s", waited, tt.waited)
				}
			})
			checkCalls(t, u, e.Repo, tt.calls(e.Repo))
			if got := editors(t, e.Home); len(got) != 0 {
				t.Errorf("ledger afterwards = %+v, want empty", got)
			}
			if got, _ := os.ReadFile(scenes); string(got) != "sceneSetups: [Main]\n" {
				t.Errorf("%s = %q, want it put back", pool.SceneListFile, got)
			}
		})
	}
}

// A ready Editor that plain closes leave running until closeRetryTimeout has passed since the
// first is closed with --force, and the instance files that leaves are removed once `unity status`
// lists no Editor in the seed (A45). The refresh succeeds, since its Editor was ready and so
// brought the Library current, and the scene list is put back (A48; #252). The waits pass on
// testing/synctest's clock.
func TestSeedRefresherForcesAReadyEditorPlainClosesCannotClose(t *testing.T) {
	e := testenv.New(t, testenv.StaleLibrary())
	files := []string{testenv.EditorInstanceFile, "Library/ProtocolInstance.json",
		"Temp/UnityLockfile"}
	e.WriteFile(testenv.EditorInstanceFile, fmt.Sprintf(`{"process_id": %d}`, launchedPID))
	e.WriteFile("Library/ProtocolInstance.json", "[]\n")
	e.WriteFile("Temp/UnityLockfile", "")
	scenes := e.Path("Library/" + pool.SceneListFile)
	e.WriteFile("Library/"+pool.SceneListFile, "sceneSetups: [Main]\n")
	// A close every closeRetryInterval from the first to the one at closeRetryTimeout.
	closes := int(closeRetryTimeout/closeRetryInterval) + 1
	replies := []fake.Reply{openHeadless(e.Repo), statusReady}
	want := refreshCalls(e.Repo, notClosed)
	for range closes {
		replies = append(replies, closeTimedOut)
		want = append(want, []string{"close", e.Repo, "--format", "json"})
	}
	replies = append(replies, closeForced, noInstances)
	want = append(want, refreshCalls(e.Repo, closedForced)[2:]...)
	u := fake.Install(t, replies...)
	synctest.Test(t, func(t *testing.T) {
		// Running for WaitReady's check and after each close that leaves it running, then gone.
		procs := newRefreshProcs(1+closes, func() {
			if err := os.WriteFile(scenes, []byte("sceneSetups: []\n"), 0o644); err != nil {
				t.Error(err)
			}
		})
		r := newRefresher(e, u, procs)
		r.Now = time.Now
		begin := time.Now()
		warnings, err := r.Refresh(t.Context(), e.Repo, false)
		if err != nil {
			t.Fatalf("Refresh: %v, want no error", err)
		}
		if waited := time.Since(begin); waited != 2*time.Minute {
			t.Errorf("Refresh waited %s, want the two minutes A48 bounds the retries by", waited)
		}
		if slices.ContainsFunc(warnings, func(w string) bool {
			return strings.Contains(w, "did not remove")
		}) {
			t.Errorf("warnings = %q, want the instance files removed", warnings)
		}
	})
	checkCalls(t, u, e.Repo, want)
	for _, name := range files {
		if _, err := os.Stat(e.Path(name)); err == nil {
			t.Errorf("%s is still there, want it removed", name)
		}
	}
	if got := editors(t, e.Home); len(got) != 0 {
		t.Errorf("ledger afterwards = %+v, want empty", got)
	}
	if got, _ := os.ReadFile(scenes); string(got) != "sceneSetups: [Main]\n" {
		t.Errorf("%s = %q, want it put back", pool.SceneListFile, got)
	}
}

// A refused admission launches nothing, and does not use up the seed's one refresh: once the
// budget admits it, the same refresher refreshes the seed.
func TestSeedRefresherRefusedLaunchesNothing(t *testing.T) {
	e := testenv.New(t, testenv.StaleLibrary())
	u := fake.Install(t, openHeadless(e.Repo), statusReady, closeHeadless)
	r := newRefresher(e, u, newRefreshProcs(1, nil))
	r.Budget.RAMGB = 1
	if _, err := r.Refresh(t.Context(), e.Repo, false); err == nil {
		t.Fatal("Refresh succeeded, want the budget to refuse it")
	}
	checkCalls(t, u, e.Repo, nil)
	if got := editors(t, e.Home); len(got) != 0 {
		t.Errorf("ledger = %+v, want empty", got)
	}
	r.Budget.RAMGB = 16
	if _, err := r.Refresh(t.Context(), e.Repo, false); err != nil {
		t.Fatalf("Refresh once the budget admits it: %v", err)
	}
	checkCalls(t, u, e.Repo, refreshCalls(e.Repo, closedPlain))
}

// A refresh of a cold seed, a slot with reimport_pending or a checkout with no Library/, is
// charged as a cold import and waits the cold-import timeout, 300 s, as `upool open` would (A20;
// A37), and ends the cold import once the Editor is ready (A39).
func TestSeedRefresherChargesAColdImport(t *testing.T) {
	for _, tt := range []struct {
		name    string
		pending bool
	}{{"reimport_pending", true}, {"no Library", false}} {
		t.Run(tt.name, func(t *testing.T) {
			e := testenv.New(t, testenv.StaleLibrary())
			if !tt.pending {
				if err := os.RemoveAll(e.Path("Library")); err != nil {
					t.Fatal(err)
				}
			}
			u := fake.Install(t, openHeadless(e.Repo), statusReady, closeHeadless)
			var charged []ledger.Editor
			// Running for the wait and for the end of the cold import.
			procs := newRefreshProcs(2, func() { charged = editors(t, e.Home) })
			if _, err := newRefresher(e, u, procs).Refresh(t.Context(), e.Repo,
				tt.pending); err != nil {
				t.Fatal(err)
			}
			checkCalls(t, u, e.Repo, refreshCallsWaiting(e.Repo, "300", closedPlain))
			if len(charged) != 1 || !charged[0].ColdImport ||
				charged[0].ChargeGB != ledger.ColdImportGB {
				t.Errorf("ledger while the Editor started = %+v, want it charged as a cold "+
					"import", charged)
			}
			if got := editors(t, e.Home); len(got) != 0 {
				t.Errorf("ledger afterwards = %+v, want empty", got)
			}
		})
	}
}

// A refresher refreshes each seed once: asked again, it fails with ErrRefreshedOnce and calls
// nothing, so that a take that retries does not open Editor after Editor in a seed its refresh
// left stale.
func TestSeedRefresherRefreshesASeedOnce(t *testing.T) {
	e := testenv.New(t, testenv.StaleLibrary())
	u := fake.Install(t, openHeadless(e.Repo), statusReady, closeHeadless)
	r := newRefresher(e, u, newRefreshProcs(1, nil))
	if _, err := r.Refresh(t.Context(), e.Repo, false); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Refresh(t.Context(), e.Repo, false); !errors.Is(err, ErrRefreshedOnce) {
		t.Errorf("second Refresh = %v, want ErrRefreshedOnce", err)
	}
	checkCalls(t, u, e.Repo, refreshCalls(e.Repo, closedPlain))
}

// The seed choice refreshes a stale candidate while it holds it, and chooses it once it is
// current; a candidate whose refresh fails, or that its refresh leaves dirty, is passed over for
// the next, the main checkout here. A slot passed over is given back ready, unless its refresh
// left it dirty, or left an Editor in it that could not be closed: it stays warming, for
// transition 22 once the cloner has exited and no Editor is live in it. A slot with
// reimport_pending is refreshed as a cold import, and the flag is cleared once it is current.
func TestChooseSeedRefreshesAStaleCandidate(t *testing.T) {
	stale := testenv.ScriptTime.Add(-time.Hour)
	refreshes := func(slot string) []fake.Reply {
		return []fake.Reply{openHeadless(slot), statusReady, closeHeadless}
	}
	compiles := func(t *testing.T, slot string) { compile(t, slot) }
	for _, tt := range []struct {
		name string
		// replies are the fake's; nil leaves unity off PATH, so the launch fails.
		replies func(slot string) []fake.Reply
		// during runs while slot-1's Editor starts.
		during  func(t *testing.T, slot string)
		pending bool
		seed    string
		state   pool.State
		wait    string
	}{
		{"refreshed and chosen", refreshes, compiles, false, "slot-1", pool.StateWarming, "120"},
		{"reimport_pending, refreshed cold and chosen", refreshes, compiles, true, "slot-1",
			pool.StateWarming, "300"},
		{"the refresh failed", nil, nil, false, "", pool.StateReady, ""},
		{"the refresh left it dirty", refreshes, func(t *testing.T, slot string) {
			compile(t, slot)
			if err := os.WriteFile(filepath.Join(slot, "left.txt"), []byte("x\n"),
				0o644); err != nil {
				t.Error(err)
			}
		}, false, "", pool.StateWarming, "120"},
		{"an Editor that could not be closed", func(slot string) []fake.Reply {
			return []fake.Reply{openHeadless(slot), statusNoInstances, statusNoInstances}
		}, nil, false, "", pool.StateWarming, "120"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := newSeedEnv(t, []pool.State{pool.StateReady}, []time.Time{stale})
			slot := e.slot(t, "slot-1").Path
			if tt.pending {
				err := pool.Update(t.Context(), e.Home, e.key, func(p *pool.Pool) error {
					p.Slots[0].ReimportPending = true
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			var u *fake.Unity
			if tt.replies != nil {
				u = fake.Install(t, tt.replies(slot)...)
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
			starts := 1
			if tt.pending {
				starts = 2 // and for the end of the cold import
			}
			e.sd.Refresher = newRefresher(e.Env, u, newRefreshProcs(starts, during))
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
			s := e.slot(t, "slot-1")
			if s.State != tt.state {
				t.Errorf("slot-1 = %s, want %s", s.State, tt.state)
			}
			if tt.seed != "" && s.ReimportPending {
				t.Error("reimport_pending is still set, want it cleared by the refresh")
			}
			if tt.seed == "" && (len(why) != 1 || !strings.HasPrefix(why[0], "slot-1: ")) {
				t.Errorf("passed over %q, want slot-1 with its reason", why)
			}
			if calls := u.Calls(); tt.wait != "" && (len(calls) < 2 || calls[1].Args[3] != tt.wait) {
				t.Errorf("unity calls = %+v, want a wait of %s s", calls, tt.wait)
			}
		})
	}
}

// A seed choice that runs again, as take's does for its timeout, does not refresh again a
// candidate its refresh left stale: the main checkout is chosen with no second Editor.
func TestChooseSeedRefreshesACandidateOnce(t *testing.T) {
	e := newSeedEnv(t, []pool.State{pool.StateReady}, []time.Time{testenv.ScriptTime.Add(
		-time.Hour)})
	slot := e.slot(t, "slot-1").Path
	u := fake.Install(t, openHeadless(slot), statusReady, closeHeadless)
	e.sd.Refresher = newRefresher(e.Env, u, newRefreshProcs(1, nil))
	for range 2 {
		seed, why, err := e.sd.Choose(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if seed != (Seed{Path: e.Repo}) {
			t.Errorf("seed = %+v, want the main checkout; passed over %q", seed, why)
		}
	}
	checkCalls(t, u, slot, refreshCalls(slot, closedPlain))
}
