package main

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/weslo/upool/internal/ledger"
	"github.com/weslo/upool/internal/output"
	"github.com/weslo/upool/internal/pathvars"
	"github.com/weslo/upool/internal/pool"
	"github.com/weslo/upool/internal/proc"
	"github.com/weslo/upool/internal/testenv"
	"github.com/weslo/upool/internal/unity/fake"
)

// The tests of `upool open`, on the harness pool (harness_test.go), with the fake unity replaying
// the captures of `unity open` and `unity status --until-ready`, and an editorProcs standing in
// for the process table, since the Editor's PID is the capture's and no process of this machine.

// capturedPID is launchedPid in testdata/captures/unity/open-headless.json and
// open-gui-streams.json, which the fake replays for `unity open`.
const (
	capturedPID    = 25147
	capturedGUIPID = 25428
)

// editorProcs is a proc.Checker over a fixed set of processes: each PID in checks has a process
// that passes as many Alive checks as its count, all of them when it is negative, and then is
// gone; any other PID is no process.
type editorProcs struct {
	mu     sync.Mutex
	checks map[int]int
	gone   map[int]bool
}

// procsAlive is an editorProcs in which pids live throughout.
func procsAlive(pids ...int) *editorProcs {
	p := &editorProcs{checks: map[int]int{}, gone: map[int]bool{}}
	for _, pid := range pids {
		p.checks[pid] = -1
	}
	return p
}

func (p *editorProcs) Start(pid int) (time.Time, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.checks[pid]; ok && !p.gone[pid] {
		return time.Unix(1, 0), nil
	}
	return time.Time{}, proc.ErrNoProcess
}

func (p *editorProcs) Alive(pid int, _ time.Time) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	n, ok := p.checks[pid]
	switch {
	case !ok || p.gone[pid]:
		return false, nil
	case n == 0:
		p.gone[pid] = true
		return false, nil
	case n > 0:
		p.checks[pid] = n - 1
	}
	return true, nil
}

// runOpen runs `upool open args...` in slot-1 of p, as runAt does, with procs as the process
// table and now as the clock.
func runOpen(t *testing.T, p harnessPool, procs proc.Checker, now func() time.Time,
	args ...string) result {
	t.Helper()
	return runInSlot1(t, p, procs, now, append([]string{"open"}, args...)...)
}

// runInSlot1 runs upool with args in slot-1 of p, as runAt does, with procs as the process table
// and now as the clock.
func runInSlot1(t *testing.T, p harnessPool, procs proc.Checker, now func() time.Time,
	args ...string) result {
	t.Helper()
	return runProcs(t, p.Env, p.slot("slot-1"), procs, now, args...)
}

// runProcs runs upool with args in e, with dir as the working directory, as runAt does, with
// procs as the process table and now as the clock.
func runProcs(t *testing.T, e *testenv.Env, dir string, procs proc.Checker, now func() time.Time,
	args ...string) result {
	t.Helper()
	var out, errOut bytes.Buffer
	d := dispatcher{
		commands:   commandTable,
		hooks:      hookTable,
		stdin:      strings.NewReader(""),
		stdout:     &out,
		stderr:     &errOut,
		getenv:     e.Getenv,
		environ:    e.Environ,
		getwd:      func() (string, error) { return dir, nil },
		now:        now,
		pid:        os.Getpid(),
		ppid:       os.Getppid(),
		executable: os.Executable,
		procs:      procs,
	}
	code := d.run(args)
	return result{code, out.String(), errOut.String()}
}

// warmPool is a harness pool whose slot-1 has a Library, so that an open of it is warm.
func warmPool(t *testing.T) harnessPool {
	t.Helper()
	p := newHarnessPool(t)
	if err := os.MkdirAll(filepath.Join(p.slot("slot-1"), "Library"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// openReply is the fake's reply to `unity open`: capture, with its stderr from the .stderr.txt
// beside it, the Hub-registry warning, which the CLI writes there and repeats in the envelope
// (A12).
func openReply(t *testing.T, e *testenv.Env, capture string) fake.Reply {
	t.Helper()
	vars := []pathvars.Var{{Name: "repo", Path: e.Repo}}
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "captures", "unity",
		strings.TrimSuffix(capture, ".json")+".stderr.txt"))
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := pathvars.Fill(string(raw), vars...)
	if err != nil {
		t.Fatal(err)
	}
	return fake.Reply{Capture: capture, Vars: vars, Stderr: stderr}
}

// statusReady and statusNoInstances are the fake's replies to `unity status --until-ready`.
var (
	statusReady = fake.Reply{Capture: "status-until-ready-ready.json",
		Vars: []pathvars.Var{{Name: "spike", Path: "/spike"}}}
	statusNoInstances = fake.Reply{Capture: "status-until-ready-no-instances.json", Exit: 6}
)

// checkOpenCalls fails the test unless u's calls are `unity open` on slot-1, with
// `--args -batchmode` when headless, then, when status, `unity status --until-ready` with timeout,
// each targeting slot-1.
func checkOpenCalls(t *testing.T, p harnessPool, u *fake.Unity, headless, status bool,
	timeout string) {
	t.Helper()
	slot := p.slot("slot-1")
	want := [][]string{{"open", slot}}
	if headless {
		want[0] = append(want[0], "--args", "-batchmode")
	}
	want[0] = append(want[0], "--format", "json")
	if status {
		want = append(want, []string{"status", "--until-ready", "--timeout", timeout, "--format",
			"json"})
	}
	calls := u.Calls()
	if !slices.EqualFunc(calls, want, func(c fake.Call, args []string) bool {
		return slices.Equal(c.Args, args) && c.ProjectPath == slot && c.Dir == slot
	}) {
		t.Errorf("unity calls = %+v, want %q, each targeting slot-1", calls, want)
	}
}

// ledgerOf returns the Editor ledger under p's home.
func ledgerOf(t *testing.T, p harnessPool) ledger.Ledger {
	t.Helper()
	l, err := ledger.Load(p.Home)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// checkEditors fails the test unless the ledger holds exactly want.
func checkEditors(t *testing.T, p harnessPool, want ...ledger.Editor) {
	t.Helper()
	if got := ledgerOf(t, p).Editors; !slices.Equal(got, want) {
		t.Errorf("editors = %+v, want %+v", got, want)
	}
}

// The acceptance test of #89, a ready reply: `upool open` in a slot with a Library opens a
// headless Editor (A36; A33), waits with `unity status --until-ready --timeout 120` (A37), and
// records one ledger entry, under the captured launchedPid, charged editor_ram_gb. The CLI's
// Hub-registry warning, on stderr and in the envelope, comes through as upool's warning (A12).
func TestOpenRecordsTheReadyEditor(t *testing.T) {
	p := warmPool(t)
	u := p.installUnity(t, openReply(t, p.Env, "open-headless.json"), statusReady)
	r := runOpen(t, p, procsAlive(os.Getpid(), capturedPID), func() time.Time { return clock },
		"--json")
	env := decode(t, r.stdout)
	if r.code != output.ExitOK || !env.Success || len(env.Warnings) != 1 ||
		!strings.Contains(env.Warnings[0], "was not found in the Hub project registry") {
		t.Errorf("exit %d, envelope %+v, stderr %q; want 0, success and the Hub warning", r.code,
			env, r.stderr)
	}
	checkOpenCalls(t, p, u, true, true, "120")
	checkEditors(t, p, ledger.Editor{Project: p.slot("slot-1"), PID: capturedPID,
		Mode: modeHeadless, OpenedByUpool: true, ChargeGB: 2.5})
}

// --gui opens the Editor without -batchmode, and records it as gui. stdout is the bare envelope
// and the warning is on stderr, as testdata/captures/unity/open-gui-streams.* recorded them
// (A12).
func TestOpenGUI(t *testing.T) {
	p := warmPool(t)
	u := p.installUnity(t, openReply(t, p.Env, "open-gui-streams.json"), statusReady)
	r := runOpen(t, p, procsAlive(os.Getpid(), capturedGUIPID),
		func() time.Time { return clock }, "--gui")
	want := "opened slot-1's Editor gui, process 25428: ready, charged 2.5 GB\n"
	if r.code != output.ExitOK || r.stdout != want {
		t.Errorf("exit %d, stdout %q, stderr %q; want 0 and %q", r.code, r.stdout, r.stderr,
			want)
	}
	checkOpenCalls(t, p, u, false, true, "120")
	checkEditors(t, p, ledger.Editor{Project: p.slot("slot-1"), PID: capturedGUIPID,
		Mode: modeGUI, OpenedByUpool: true, ChargeGB: 2.5})
}

// A slot with no Library, or with reimport_pending set, is opened as a cold import (A20), with
// the cold-import timeout (A37). Once the Editor is ready the cold import ends: the entry is
// charged editor_ram_gb, and reimport_pending is cleared (A39).
func TestOpenChargesAColdImportUntilReady(t *testing.T) {
	for _, tt := range []struct {
		name    string
		prepare func(t *testing.T, p harnessPool)
	}{
		{"no Library", func(*testing.T, harnessPool) {}},
		{"reimport_pending", func(t *testing.T, p harnessPool) {
			if err := os.MkdirAll(filepath.Join(p.slot("slot-1"), "Library"), 0o755); err != nil {
				t.Fatal(err)
			}
			p.update(t, func(pl *pool.Pool) { pl.Slots[0].ReimportPending = true })
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := newHarnessPool(t)
			tt.prepare(t, p)
			u := p.installUnity(t, openReply(t, p.Env, "open-headless.json"), statusReady)
			r := runOpen(t, p, procsAlive(os.Getpid(), capturedPID),
				func() time.Time { return clock }, "--json")
			env := decode(t, r.stdout)
			data, _ := env.Data.(map[string]any)
			if r.code != output.ExitOK || data["cold_import"] != true || data["charge_gb"] != 2.5 {
				t.Errorf("exit %d, data %+v, stderr %q; want 0, cold_import true and a "+
					"charge of 2.5", r.code, env.Data, r.stderr)
			}
			checkOpenCalls(t, p, u, true, true, "300")
			checkEditors(t, p, ledger.Editor{Project: p.slot("slot-1"), PID: capturedPID,
				Mode: modeHeadless, OpenedByUpool: true, ChargeGB: 2.5})
			if s := p.loadPool(t).Slots[0]; s.ReimportPending {
				t.Error("reimport_pending is still set")
			}
		})
	}
}

// The acceptance test of #89, an Editor that exits during the wait: the status call is still
// waiting, held by the fake, when the Editor's process is gone at the third check, so open ends
// the call and fails long before the timeout, with no ledger entry left.
func TestOpenFailsWhenTheEditorExits(t *testing.T) {
	p := warmPool(t)
	held := statusNoInstances
	held.Hold = time.Hour
	u := p.installUnity(t, openReply(t, p.Env, "open-headless.json"), held)
	procs := procsAlive(os.Getpid())
	procs.checks[capturedPID] = 2 // running for two checks, a second apart, then gone
	begin := time.Now()
	r := runOpen(t, p, procs, func() time.Time { return clock })
	want := "upool: the Editor of " + p.slot("slot-1") + ", process 25147, exited before it " +
		"was ready\n"
	if r.code != output.ExitFailure || !strings.HasSuffix(r.stderr, want) {
		t.Errorf("exit %d, stderr %q; want 1 and %q", r.code, r.stderr, want)
	}
	if took := time.Since(begin); took > 30*time.Second {
		t.Errorf("open took %s, want it to fail well before the 120 s timeout", took)
	}
	checkOpenCalls(t, p, u, true, true, "120")
	checkEditors(t, p)
}

// An Editor that is not ready when `unity status --until-ready` gives up fails as the status call
// does, exiting 6 with STATUS_NO_INSTANCES (A3), and leaves no ledger entry.
func TestOpenFailsWhenTheEditorIsNotReadyInTime(t *testing.T) {
	p := warmPool(t)
	u := p.installUnity(t, openReply(t, p.Env, "open-headless.json"), statusNoInstances)
	r := runOpen(t, p, procsAlive(os.Getpid(), capturedPID), func() time.Time { return clock },
		"--json")
	env := decode(t, r.stdout)
	if r.code != output.ExitNoTarget || len(env.Errors) != 1 ||
		env.Errors[0].Code != "STATUS_NO_INSTANCES" ||
		!strings.HasPrefix(env.Errors[0].Message, "the Editor of "+p.slot("slot-1")+
			" was not ready after 2m0s: ") {
		t.Errorf("exit %d, envelope %+v; want 6 and STATUS_NO_INSTANCES", r.code, env)
	}
	checkOpenCalls(t, p, u, true, true, "120")
	checkEditors(t, p)
}

// A `unity open` that fails leaves no ledger entry and waits for nothing.
func TestOpenFailsWhenTheLaunchFails(t *testing.T) {
	p := warmPool(t)
	u := p.installUnity(t, fake.Reply{Capture: "version.raw.txt", Exit: 1})
	r := runOpen(t, p, procsAlive(os.Getpid()), func() time.Time { return clock })
	if r.code != output.ExitFailure ||
		!strings.Contains(r.stderr, "with a reply that is not JSON") {
		t.Errorf("exit %d, stderr %q; want 1 and the failed launch", r.code, r.stderr)
	}
	checkOpenCalls(t, p, u, true, false, "")
	checkEditors(t, p)
}

// The acceptance test of #89, a refused admission: with max_editors' three Editors open, open
// fails at once naming the limit, makes no unity call (the fake answers none), and leaves the
// ledger as it was (A38).
func TestOpenRefusedByTheBudgetCallsNoUnity(t *testing.T) {
	p := warmPool(t)
	p.installUnity(t)
	var open []ledger.Editor
	for i, n := range []string{"slot-2", "slot-3", "slot-4"} {
		open = append(open, ledger.Editor{Project: p.Path(".claude/worktrees/" + n),
			PID: 1001 + i, Mode: modeGUI, OpenedByUpool: true, ChargeGB: 2.5})
	}
	writeLedger(t, p, open...)
	r := runOpen(t, p, procsAlive(os.Getpid(), 1001, 1002, 1003),
		func() time.Time { return clock })
	if r.code != output.ExitFailure || !strings.Contains(r.stderr,
		"max_editors refuses another Editor: 3 Editors are open, and it allows 3") {
		t.Errorf("exit %d, stderr %q; want 1 and the max_editors refusal", r.code, r.stderr)
	}
	checkEditors(t, p, open...)
}

// A refusal by max_cold_imports waits for the running cold import, saying so once on stderr, and
// fails after the cold-import timeout, naming the limit, with no unity call (A38). The wait passes
// on testing/synctest's clock.
func TestOpenWaitsForTheRunningColdImport(t *testing.T) {
	p := newHarnessPool(t) // slot-1 has no Library, so this open is a cold import
	p.installUnity(t)
	importing := ledger.Editor{Project: p.slot("slot-2"), PID: 1001, Mode: modeHeadless,
		OpenedByUpool: true, ColdImport: true, ChargeGB: 4.5}
	writeLedger(t, p, importing)
	synctest.Test(t, func(t *testing.T) {
		begin := time.Now()
		r := runOpen(t, p, procsAlive(os.Getpid(), 1001), time.Now)
		if waited := time.Since(begin); waited != coldReadyTimeout {
			t.Errorf("open waited %s, want %s", waited, coldReadyTimeout)
		}
		waiting := "upool: waiting to open the Editor of " + p.slot("slot-1") +
			": max_cold_imports refuses another Editor: 1 cold imports are running, and it " +
			"allows 1 at once\n"
		failed := "upool: opening the Editor of " + p.slot("slot-1") + ": still refused after " +
			"5m0s: max_cold_imports refuses another Editor: 1 cold imports are running, and it " +
			"allows 1 at once\n"
		if r.code != output.ExitFailure || r.stderr != waiting+failed {
			t.Errorf("exit %d, stderr %q; want 1 and\n%s%s", r.code, r.stderr, waiting, failed)
		}
	})
	checkEditors(t, p, importing)
}

// A cold-import open admitted once the running cold import has ended goes ahead: here the
// importing Editor's process is gone at the third admission, two seconds in, so the ledger drops
// it.
func TestOpenGoesAheadWhenTheColdImportEnds(t *testing.T) {
	p := newHarnessPool(t)
	u := p.installUnity(t, openReply(t, p.Env, "open-headless.json"), statusReady)
	writeLedger(t, p, ledger.Editor{Project: p.slot("slot-2"), PID: 1001, Mode: modeHeadless,
		OpenedByUpool: true, ColdImport: true, ChargeGB: 4.5})
	procs := &dyingStart{editorProcs: procsAlive(os.Getpid(), capturedPID), pid: 1001, starts: 2}
	synctest.Test(t, func(t *testing.T) {
		begin := time.Now()
		r := runOpen(t, p, procs, time.Now)
		if r.code != output.ExitOK {
			t.Errorf("exit %d, stderr %q; want 0", r.code, r.stderr)
		}
		if waited := time.Since(begin); waited != 2*retryInterval {
			t.Errorf("open waited %s, want %s", waited, 2*retryInterval)
		}
	})
	checkOpenCalls(t, p, u, true, true, "300")
	checkEditors(t, p, ledger.Editor{Project: p.slot("slot-1"), PID: capturedPID,
		Mode: modeHeadless, OpenedByUpool: true, ChargeGB: 2.5})
}

// dyingStart is an editorProcs in which pid has a process for its first starts Start calls only,
// as a cold-importing Editor that exits while another open waits for it.
type dyingStart struct {
	*editorProcs
	pid, starts int
}

func (d *dyingStart) Start(pid int) (time.Time, error) {
	if pid == d.pid {
		d.mu.Lock()
		defer d.mu.Unlock()
		if d.starts == 0 {
			return time.Time{}, proc.ErrNoProcess
		}
		d.starts--
		return time.Unix(1, 0), nil
	}
	return d.editorProcs.Start(pid)
}

// writeLedger writes editors as the ledger under p's home.
func writeLedger(t *testing.T, p harnessPool, editors ...ledger.Editor) {
	t.Helper()
	if err := ledger.Update(t.Context(), p.Home, func(l *ledger.Ledger) error {
		l.Editors = editors
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// open works only inside a slot, and takes at most one mode.
func TestOpenRefusesWhatItCannotOpen(t *testing.T) {
	p := warmPool(t)
	p.installUnity(t)
	r := runIn(t, p.Env, p.Repo, "open", "--json")
	env := decode(t, r.stdout)
	if r.code != output.ExitFailure || len(env.Errors) != 1 ||
		env.Errors[0].Code != string(output.CodeNotInSlot) {
		t.Errorf("open in the main checkout: exit %d, envelope %+v; want 1 and NOT_IN_SLOT",
			r.code, env)
	}
	r = runOpen(t, p, procsAlive(os.Getpid()), func() time.Time { return clock }, "--gui",
		"--headless")
	if r.code != output.ExitFailure || !strings.Contains(r.stderr, "at most one of --headless "+
		"and --gui") {
		t.Errorf("open --gui --headless: exit %d, stderr %q; want it refused", r.code, r.stderr)
	}
	checkEditors(t, p)
}
