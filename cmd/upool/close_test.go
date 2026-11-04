package main

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/weslo/upool/internal/ledger"
	"github.com/weslo/upool/internal/output"
	"github.com/weslo/upool/internal/pathvars"
	"github.com/weslo/upool/internal/testenv"
	"github.com/weslo/upool/internal/unity"
	"github.com/weslo/upool/internal/unity/fake"
)

// The tests of `upool close`, on the harness pool with an Editor recorded in slot-1's ledger
// entry, the fake unity replaying the captures of the scene listing, save_all and `unity close`,
// and an editorProcs for the process table, since the Editor's PID is the capture's.

// closedPID is the pid testdata/captures/unity/close-gui.json reports, the Editor the tests'
// ledger records in slot-1.
const closedPID = 10648

// forcedPID is the pid testdata/captures/unity/close-unreachable.json and
// close-force-unreachable.json report: the Editor `close --force` closes.
const forcedPID = 60422

// spikeVars fill ${spike} in the save_all and close captures.
var spikeVars = []pathvars.Var{{Name: "spike", Path: "/spike"}}

// evalReply is the fake's reply to one of close's evals (#280): the captured reply of an eval,
// testdata/captures/unity/command-eval-first-call.json, with returned as the string its code
// returned. No real Editor's reply to close's own code is captured yet (testdata/README.md, the
// pending rows), so the string is the one upool's code returns: for the listing, one scene a line,
// "1" or "0" for unsaved changes, a tab and the path, as unity.Client.ListScenes reads it.
func evalReply(returned string) fake.Reply {
	quoted, _ := json.Marshal(returned) // a string always encodes
	return fake.Reply{Capture: "command-eval-first-call.json",
		Vars:    []pathvars.Var{{Name: "repo", Path: "/repo"}},
		Replace: []string{`"result": "6000.4.11f1"`, `"result": ` + string(quoted)}}
}

// The fake's replies to the scene listing, to save_all and to `unity close`. titledScenes lists one
// scene, the save_all capture's, which has a path and unsaved changes.
var (
	titledScenes = evalReply("1\tAssets/Down/States/Startup/Startup.unity\n")
	saveAllReply = fake.Reply{Capture: "command-save-all.json", Vars: spikeVars}
	closeReply   = fake.Reply{Capture: "close-gui.json", Vars: spikeVars}
	// mainThreadTimeout is the Editor's main-thread timeout, whose exit no capture records
	// (docs/contracts.md (d)).
	mainThreadTimeout = fake.Reply{Capture: "command-eval-main-thread-timeout.json", Exit: 1}
)

// The calls of close's evals as callArgs writes them, the code replaced by what it does: the
// listing of the open scenes, and the save of the one scene at a path (savingArgs).
var (
	listingArgs = []string{"command", "eval", "<list the open scenes>", "--format", "json"}
	saveAllArgs = []string{"command", "save_all", "--format", "json"}
)

// savingArgs is the call of the eval that saves the open scene at path alone, as callArgs writes
// it.
func savingArgs(path string) []string {
	return []string{"command", "eval", "<save " + path + ">", "--format", "json"}
}

// evalShape is args with an eval's code replaced by what the code does, the listing or the save of
// one scene, so that a test can compare calls without spelling out the C#.
func evalShape(args []string) []string {
	if len(args) < 3 || args[0] != "command" || args[1] != "eval" {
		return args
	}
	shaped := slices.Clone(args)
	if strings.Contains(args[2], "SceneManager.GetSceneAt(i)") {
		shaped[2] = listingArgs[2]
	} else if _, rest, ok := strings.Cut(args[2], `GetSceneByPath(@"`); ok {
		path, _, _ := strings.Cut(rest, `")`)
		shaped[2] = savingArgs(strings.ReplaceAll(path, `""`, `"`))[2]
	}
	return shaped
}

// closePool is a harness pool whose ledger records a GUI Editor in slot-1, with a fake unity on
// PATH answering with replies.
func closePool(t *testing.T, replies ...fake.Reply) (harnessPool, *fake.Unity, ledger.Editor) {
	t.Helper()
	p := newHarnessPool(t)
	u := p.installUnity(t, replies...)
	e := ledger.Editor{Project: p.slot("slot-1"), PID: closedPID, Mode: modeGUI,
		OpenedByUpool: true, ChargeGB: 2.5}
	writeLedger(t, p, e)
	return p, u, e
}

// callArgs are the arguments of u's calls, in order, each eval's as evalShape writes it, failing
// the test unless each targets slot-1.
func callArgs(t *testing.T, p harnessPool, u *fake.Unity) [][]string {
	t.Helper()
	var args [][]string
	for _, c := range u.Calls() {
		if c.ProjectPath != p.slot("slot-1") || c.Dir != p.slot("slot-1") {
			t.Errorf("unity %v with UNITY_PROJECT_PATH %q in %s, want it targeting slot-1",
				c.Args, c.ProjectPath, c.Dir)
		}
		args = append(args, evalShape(c.Args))
	}
	return args
}

// checkCloseCalls fails the test unless u's calls are want, in order.
func checkCloseCalls(t *testing.T, p harnessPool, u *fake.Unity, want ...[]string) {
	t.Helper()
	if got := callArgs(t, p, u); !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("unity calls = %q, want %q", got, want)
	}
}

// The acceptance test of #90, as #280 has it: the open scenes listed, all with a path, then
// save_all, then `unity close <slot>` with the slot positional; close then waits for the Editor's
// process to be gone, here two seconds after the close returns, and takes the Editor out of the
// ledger. The wait passes on testing/synctest's clock.
func TestCloseSavesThenCloses(t *testing.T) {
	p, u, _ := closePool(t, titledScenes, saveAllReply, closeReply)
	procs := &dyingStart{editorProcs: procsAlive(), pid: closedPID, starts: 2}
	synctest.Test(t, func(t *testing.T) {
		begin := time.Now()
		r := runInSlot1(t, p, procs, time.Now, "close")
		want := "saved and closed slot-1's Editor, process 10648\n"
		if r.code != output.ExitOK || r.stdout != want || r.stderr != "" {
			t.Errorf("exit %d, stdout %q, stderr %q; want 0, %q and nothing on stderr", r.code,
				r.stdout, r.stderr, want)
		}
		if waited := time.Since(begin); waited != 2*unity.ExitPollInterval {
			t.Errorf("close waited %s for the process, want %s", waited, 2*unity.ExitPollInterval)
		}
	})
	checkCloseCalls(t, p, u, listingArgs, saveAllArgs,
		[]string{"close", p.slot("slot-1"), "--format", "json"})
	checkEditors(t, p)
}

// The acceptance test of #280, a scene with no path: close does not save it, and one line on
// stderr says so for each, whether it has unsaved changes or none. save_all would save it too, so
// the scenes that have a path and unsaved changes are saved one by one instead, and the scene with
// a path and none is not saved at all; then the Editor is closed.
func TestCloseSkipsScenesWithNoPath(t *testing.T) {
	line := func(n int, changes string) string {
		return fmt.Sprintf("upool: warning: not saving open scene %d of 4 in slot-1's Editor, "+
			"which is Untitled: it has no path, and saving it would open a Save panel; %s\n", n,
			changes)
	}
	listed := evalReply("1\tAssets/A.unity\n0\t\n0\tAssets/B.unity\n1\t\n")
	p, u, _ := closePool(t, listed, evalReply("saved"), closeReply)
	r := runInSlot1(t, p, procsAlive(), func() time.Time { return clock }, "close")
	want := line(2, "it has no unsaved changes") +
		line(4, "its unsaved changes are discarded when the Editor closes")
	if r.code != output.ExitOK || r.stdout != "saved and closed slot-1's Editor, process 10648\n" ||
		!strings.HasPrefix(r.stderr, want) {
		t.Errorf("exit %d, stdout %q, stderr %q; want 0 and stderr starting\n%s", r.code, r.stdout,
			r.stderr, want)
	}
	checkCloseCalls(t, p, u, listingArgs, savingArgs("Assets/A.unity"),
		[]string{"close", p.slot("slot-1"), "--format", "json"})
	checkEditors(t, p)
}

// An Editor whose only open scene has no path, as after the restore that follows a development
// build on the first benchmark run, has nothing close can save: the line, then the close, with no
// save call at all (#280).
func TestCloseSavesNothingWhenNoSceneHasAPath(t *testing.T) {
	p, u, _ := closePool(t, evalReply("0\t\n"), closeReply)
	r := runInSlot1(t, p, procsAlive(), func() time.Time { return clock }, "close", "--json")
	env := decode(t, r.stdout)
	want := "not saving open scene 1 of 1 in slot-1's Editor, which is Untitled: it has no " +
		"path, and saving it would open a Save panel; it has no unsaved changes"
	if r.code != output.ExitOK || !slices.Equal(env.Warnings, []string{want}) {
		t.Errorf("exit %d, warnings %q; want 0 and %q", r.code, env.Warnings, want)
	}
	checkCloseCalls(t, p, u, listingArgs,
		[]string{"close", p.slot("slot-1"), "--format", "json"})
	checkEditors(t, p)
}

// A listing that fails any way but a timeout is a warning, and save_all saves every scene, as
// before close listed them: here the reply of an Editor version whose eval returns no listing.
func TestCloseSavesAllWhenItCannotListTheScenes(t *testing.T) {
	p, u, _ := closePool(t, evalReply("not a listing"), saveAllReply, closeReply)
	r := runInSlot1(t, p, procsAlive(), func() time.Time { return clock }, "close")
	warning := "upool: warning: could not list the scenes open in slot-1's Editor, so save_all " +
		"saves them all: "
	if r.code != output.ExitOK || !strings.HasPrefix(r.stderr, warning) {
		t.Errorf("exit %d, stderr %q; want 0 and %q…", r.code, r.stderr, warning)
	}
	checkCloseCalls(t, p, u, listingArgs, saveAllArgs,
		[]string{"close", p.slot("slot-1"), "--format", "json"})
}

// The acceptance test of #90, a failing save: close stops there and exits 1, whatever the save's
// own exit, makes no `unity close` call and leaves the ledger as it was. With no Editor, the
// listing fails too, which is a warning before the save fails.
func TestCloseNeverClosesAfterAFailedSave(t *testing.T) {
	noEditor := fake.Reply{Capture: "command-eval-no-pipeline-instance.json", Vars: spikeVars,
		Exit: 6}
	for _, tt := range []struct {
		name    string
		replies []fake.Reply
		calls   [][]string
	}{
		{"no Editor", []fake.Reply{noEditor, noEditor}, [][]string{listingArgs, saveAllArgs}},
		{"save_all failed", []fake.Reply{titledScenes, noEditor},
			[][]string{listingArgs, saveAllArgs}},
		{"one scene not saved", []fake.Reply{evalReply("1\tAssets/A.unity\n0\t\n"),
			evalReply("not saved")}, [][]string{listingArgs, savingArgs("Assets/A.unity")}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p, u, e := closePool(t, tt.replies...)
			r := runInSlot1(t, p, procsAlive(closedPID), func() time.Time { return clock },
				"close")
			want := "upool: not closing the Editor of " + p.slot("slot-1") + ", since saving " +
				"its scenes failed: "
			if r.code != output.ExitFailure || !strings.Contains(r.stderr, want) {
				t.Errorf("exit %d, stderr %q; want 1 and %q…", r.code, r.stderr, want)
			}
			checkCloseCalls(t, p, u, tt.calls...)
			checkEditors(t, p, e)
		})
	}
}

// The acceptance test of #280, a save that times out: the Editor's main thread timed out on the
// save and on its re-query (A29), as a native panel holding it makes it do, whether the save was
// save_all, the listing before it, or the save of one scene. close says that a panel is the
// likely cause and what to do, makes no `unity close` call and leaves the ledger as it was. A
// save that gets no answer within unity.SaveTimeout is the same failure (internal/unity).
func TestCloseNamesAPanelWhenItsSaveTimesOut(t *testing.T) {
	for _, tt := range []struct {
		name    string
		replies []fake.Reply
		calls   [][]string
	}{
		{"save_all", []fake.Reply{titledScenes, mainThreadTimeout, mainThreadTimeout},
			[][]string{listingArgs, saveAllArgs, saveAllArgs}},
		{"listing", []fake.Reply{mainThreadTimeout, mainThreadTimeout},
			[][]string{listingArgs, listingArgs}},
		{"one scene", []fake.Reply{evalReply("1\tAssets/A.unity\n1\t\n"), mainThreadTimeout,
			mainThreadTimeout}, [][]string{listingArgs, savingArgs("Assets/A.unity"),
			savingArgs("Assets/A.unity")}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p, u, e := closePool(t, tt.replies...)
			r := runInSlot1(t, p, procsAlive(closedPID), func() time.Time { return clock },
				"close")
			want := "upool: not closing the Editor of " + p.slot("slot-1") + ", since saving " +
				"its scenes timed out: a native panel open in the Editor, such as the Save panel " +
				"a scene with no path opens, is the likely cause. Press Cancel in slot-1's " +
				"Editor, then run `upool close` again; if nothing in it is unsaved, `upool close " +
				"--force` in the slot closes it without saving ("
			if r.code != output.ExitFailure || !strings.Contains(r.stderr, want) ||
				!strings.Contains(r.stderr, "Main thread operation timed out") {
				t.Errorf("exit %d, stderr %q; want 1 and\n%s…", r.code, r.stderr, want)
			}
			checkCloseCalls(t, p, u, tt.calls...)
			checkEditors(t, p, e)
		})
	}
}

// The panel advice is for an Editor with a window. A headless Editor, as `upool open` opens by
// default in batch mode (A33; A36), has none, so its save that times out is the plain failure:
// no Cancel to press, and no --force, which would discard what a later close could save. An
// Editor the ledger does not record may have a window, and gets the advice.
func TestCloseGivesPanelAdviceOnlyToAnEditorWithAWindow(t *testing.T) {
	advice := "Press Cancel in slot-1's Editor"
	for _, tt := range []struct {
		name    string
		editors func(p harnessPool) []ledger.Editor
		advised bool
	}{
		{"headless", func(p harnessPool) []ledger.Editor {
			return []ledger.Editor{{Project: p.slot("slot-1"), PID: closedPID, Mode: modeHeadless,
				OpenedByUpool: true, ChargeGB: 2.5}}
		}, false},
		{"not in the ledger", func(harnessPool) []ledger.Editor { return nil }, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p, u, _ := closePool(t, titledScenes, mainThreadTimeout, mainThreadTimeout)
			writeLedger(t, p, tt.editors(p)...)
			r := runInSlot1(t, p, procsAlive(closedPID), func() time.Time { return clock },
				"close")
			want := "upool: not closing the Editor of " + p.slot("slot-1") + ", since saving " +
				"its scenes failed: "
			if tt.advised {
				want = "upool: not closing the Editor of " + p.slot("slot-1") + ", since saving " +
					"its scenes timed out: a native panel open in the Editor"
			}
			if r.code != output.ExitFailure || !strings.Contains(r.stderr, want) ||
				!strings.Contains(r.stderr, "Main thread operation timed out") ||
				strings.Contains(r.stderr, advice) != tt.advised ||
				strings.Contains(r.stderr, "--force") != tt.advised {
				t.Errorf("exit %d, stderr %q; want 1 and %q…, with the advice %t", r.code,
					r.stderr, want, tt.advised)
			}
			checkCloseCalls(t, p, u, listingArgs, saveAllArgs, saveAllArgs)
			checkEditors(t, p, tt.editors(p)...)
		})
	}
}

// The acceptance test of #280, --force: close sends `unity close <slot> --force` and no save_all,
// nor any listing; once the Editor is gone and `unity status` lists none in the slot, it removes
// the instance files a forced close leaves (A45), and takes the Editor out of the ledger. It says
// it did not save, and times the close alone.
func TestCloseForceClosesWithoutSaving(t *testing.T) {
	p := newHarnessPool(t)
	u := p.installUnity(t, closeForcedReply, noEditorsReply)
	writeLedger(t, p, ledger.Editor{Project: p.slot("slot-1"), PID: forcedPID, Mode: modeGUI,
		OpenedByUpool: true, ChargeGB: 2.5})
	files := writeInstanceFiles(t, p, forcedPID)
	r := runInSlot1(t, p, procsAlive(), func() time.Time { return clock }, "close", "--force",
		"--json")
	env := decode(t, r.stdout)
	data, _ := env.Data.(map[string]any)
	if r.code != output.ExitOK || data["saved"] != false || data["method"] != "forced" ||
		data["pid"] != float64(forcedPID) || len(env.Warnings) != 0 {
		t.Errorf("exit %d, envelope %+v; want 0, saved false, method forced, pid %d and no "+
			"warnings", r.code, env, forcedPID)
	}
	if env.Timings == nil || len(env.Timings.Steps) != 1 || env.Timings.Steps[0].Name != "close" {
		t.Errorf("timings = %+v, want the close step alone", env.Timings)
	}
	checkCloseCalls(t, p, u, []string{"close", p.slot("slot-1"), "--force", "--format", "json"},
		[]string{"status", "--format", "json"})
	for _, name := range files {
		if _, err := os.Stat(p.Path(name)); err == nil {
			t.Errorf("%s is still there, want it removed", name)
		}
	}
	checkEditors(t, p)
}

// --force is the way out of an Editor the plain close cannot reach: a close whose save went
// through but whose `unity close` found no graceful-close channel fails, exiting 6, and leaves the
// Editor in the ledger; `close --force` then closes it, saying so on stdout (#280; #224).
func TestCloseForceClosesWhatThePlainCloseCouldNot(t *testing.T) {
	p := newHarnessPool(t)
	u := p.installUnity(t, titledScenes, saveAllReply, closeUnreachableReply, closeForcedReply,
		noEditorsReply)
	e := ledger.Editor{Project: p.slot("slot-1"), PID: forcedPID, Mode: modeGUI,
		OpenedByUpool: true, ChargeGB: 2.5}
	writeLedger(t, p, e)
	now := func() time.Time { return clock }
	r := runInSlot1(t, p, procsAlive(forcedPID), now, "close")
	if r.code != output.ExitNoTarget || !strings.Contains(r.stderr, "PROJECTS_CLOSE_UNREACHABLE") {
		t.Errorf("close: exit %d, stderr %q; want 6 and the unreachable close", r.code, r.stderr)
	}
	checkEditors(t, p, e)
	r = runInSlot1(t, p, procsAlive(), now, "close", "--force")
	want := "closed slot-1's Editor without saving, process 60422\n"
	if r.code != output.ExitOK || r.stdout != want {
		t.Errorf("close --force: exit %d, stdout %q, stderr %q; want 0 and %q", r.code, r.stdout,
			r.stderr, want)
	}
	slot := p.slot("slot-1")
	checkCloseCalls(t, p, u, listingArgs, saveAllArgs, []string{"close", slot, "--format", "json"},
		[]string{"close", slot, "--force", "--format", "json"},
		[]string{"status", "--format", "json"})
	checkEditors(t, p)
}

// writeInstanceFiles writes the instance files an Editor under pid leaves in slot-1 when it is
// terminated, and returns their names.
func writeInstanceFiles(t *testing.T, p harnessPool, pid int) []string {
	t.Helper()
	const slot = ".claude/worktrees/slot-1/"
	files := []string{slot + testenv.EditorInstanceFile, slot + "Library/ProtocolInstance.json",
		slot + "Temp/UnityLockfile"}
	p.WriteFile(files[0], fmt.Sprintf(`{"process_id": %d}`, pid))
	p.WriteFile(files[1], "[]\n")
	p.WriteFile(files[2], "")
	return files
}

// An Editor still running unity.ExitTimeout after `unity close` returned fails close, and stays in the
// ledger, since it is still up and still charged.
func TestCloseFailsWhileTheEditorRuns(t *testing.T) {
	p, _, e := closePool(t, titledScenes, saveAllReply, closeReply)
	synctest.Test(t, func(t *testing.T) {
		begin := time.Now()
		r := runInSlot1(t, p, procsAlive(closedPID), time.Now, "close")
		want := "upool: unity close " + p.slot("slot-1") + " returned, but the Editor's " +
			"process [10648] was still running 30s later\n"
		if r.code != output.ExitFailure || r.stderr != want {
			t.Errorf("exit %d, stderr %q; want 1 and %q", r.code, r.stderr, want)
		}
		if waited := time.Since(begin); waited != unity.ExitTimeout {
			t.Errorf("close waited %s, want %s", waited, unity.ExitTimeout)
		}
	})
	checkEditors(t, p, e)
}

// A `unity close` that fails is close's failure, with the ledger left as it was.
func TestCloseFailsWhenUnityCloseFails(t *testing.T) {
	p, _, e := closePool(t, titledScenes, saveAllReply, fake.Reply{Capture: "version.raw.txt"})
	r := runInSlot1(t, p, procsAlive(closedPID), func() time.Time { return clock }, "close")
	if r.code != output.ExitFailure || !strings.Contains(r.stderr,
		"unity close "+p.slot("slot-1")+" exited 0 with a reply that is not JSON") {
		t.Errorf("exit %d, stderr %q; want 1 and the failed close", r.code, r.stderr)
	}
	checkEditors(t, p, e)
}

// close works only inside a slot, and takes no arguments.
func TestCloseRefusesWhatItCannotClose(t *testing.T) {
	p, _, e := closePool(t)
	r := runIn(t, p.Env, p.Repo, "close", "--json")
	env := decode(t, r.stdout)
	if r.code != output.ExitFailure || len(env.Errors) != 1 ||
		env.Errors[0].Code != string(output.CodeNotInSlot) {
		t.Errorf("close in the main checkout: exit %d, envelope %+v; want 1 and NOT_IN_SLOT",
			r.code, env)
	}
	r = runInSlot1(t, p, procsAlive(os.Getpid()), func() time.Time { return clock }, "close",
		"slot-2")
	if r.code != output.ExitFailure || !strings.Contains(r.stderr, "close takes no arguments") {
		t.Errorf("close slot-2: exit %d, stderr %q; want it refused", r.code, r.stderr)
	}
	checkEditors(t, p, e)
}
