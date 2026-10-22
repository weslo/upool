package main

import (
	"os"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/weslo/upool/internal/ledger"
	"github.com/weslo/upool/internal/output"
	"github.com/weslo/upool/internal/pathvars"
	"github.com/weslo/upool/internal/unity"
	"github.com/weslo/upool/internal/unity/fake"
)

// The tests of `upool close`, on the harness pool with an Editor recorded in slot-1's ledger
// entry, the fake unity replaying the captures of save_all and `unity close`, and an editorProcs
// for the process table, since the Editor's PID is the capture's.

// closedPID is the pid testdata/captures/unity/close-gui.json reports, the Editor the tests'
// ledger records in slot-1.
const closedPID = 10648

// spikeVars fill ${spike} in the save_all and close captures.
var spikeVars = []pathvars.Var{{Name: "spike", Path: "/spike"}}

// The fake's replies to save_all and to `unity close`.
var (
	saveAllReply = fake.Reply{Capture: "command-save-all.json", Vars: spikeVars}
	closeReply   = fake.Reply{Capture: "close-gui.json", Vars: spikeVars}
)

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

// callArgs are the arguments of u's calls, in order, failing the test unless each targets slot-1.
func callArgs(t *testing.T, p harnessPool, u *fake.Unity) [][]string {
	t.Helper()
	var args [][]string
	for _, c := range u.Calls() {
		if c.ProjectPath != p.slot("slot-1") || c.Dir != p.slot("slot-1") {
			t.Errorf("unity %v with UNITY_PROJECT_PATH %q in %s, want it targeting slot-1",
				c.Args, c.ProjectPath, c.Dir)
		}
		args = append(args, c.Args)
	}
	return args
}

// The acceptance test of #90: save_all, then `unity close <slot>` with the slot positional; close
// then waits for the Editor's process to be gone, here two seconds after the close returns, and
// takes the Editor out of the ledger. The wait passes on testing/synctest's clock.
func TestCloseSavesThenCloses(t *testing.T) {
	p, u, _ := closePool(t, saveAllReply, closeReply)
	procs := &dyingStart{editorProcs: procsAlive(), pid: closedPID, starts: 2}
	synctest.Test(t, func(t *testing.T) {
		begin := time.Now()
		r := runInSlot1(t, p, procs, time.Now, "close")
		want := "saved and closed slot-1's Editor, process 10648\n"
		if r.code != output.ExitOK || r.stdout != want {
			t.Errorf("exit %d, stdout %q, stderr %q; want 0 and %q", r.code, r.stdout, r.stderr,
				want)
		}
		if waited := time.Since(begin); waited != 2*unity.ExitPollInterval {
			t.Errorf("close waited %s for the process, want %s", waited, 2*unity.ExitPollInterval)
		}
	})
	want := [][]string{{"command", "save_all", "--format", "json"},
		{"close", p.slot("slot-1"), "--format", "json"}}
	if got := callArgs(t, p, u); !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("unity calls = %q, want save_all, then close with the slot positional", got)
	}
	checkEditors(t, p)
}

// The acceptance test of #90, a failing save_all: close stops there and exits 1, whatever the
// save's own exit, makes no `unity close` call and leaves the ledger as it was. A save that times
// out twice, re-queried once (A29), stops it the same way.
func TestCloseNeverClosesAfterAFailedSave(t *testing.T) {
	timeout := fake.Reply{Capture: "command-eval-main-thread-timeout.json", Exit: 1}
	for _, tt := range []struct {
		name    string
		replies []fake.Reply
		saves   int
	}{
		{"no Editor", []fake.Reply{{Capture: "command-eval-no-pipeline-instance.json",
			Vars: spikeVars, Exit: 6}}, 1},
		{"two timeouts", []fake.Reply{timeout, timeout}, 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p, u, e := closePool(t, tt.replies...)
			r := runInSlot1(t, p, procsAlive(closedPID), func() time.Time { return clock },
				"close")
			want := "upool: not closing the Editor of " + p.slot("slot-1") + ", since saving " +
				"its scenes failed: "
			if r.code != output.ExitFailure || !strings.HasPrefix(r.stderr, want) {
				t.Errorf("exit %d, stderr %q; want 1 and %q…", r.code, r.stderr, want)
			}
			got := callArgs(t, p, u)
			if len(got) != tt.saves || slices.ContainsFunc(got, func(a []string) bool {
				return a[0] != "command"
			}) {
				t.Errorf("unity calls = %q, want %d save_all and no close", got, tt.saves)
			}
			checkEditors(t, p, e)
		})
	}
}

// An Editor still running unity.ExitTimeout after `unity close` returned fails close, and stays in the
// ledger, since it is still up and still charged.
func TestCloseFailsWhileTheEditorRuns(t *testing.T) {
	p, _, e := closePool(t, saveAllReply, closeReply)
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
	p, _, e := closePool(t, saveAllReply, fake.Reply{Capture: "version.raw.txt"})
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
