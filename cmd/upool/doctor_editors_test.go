package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/weslo/upool/internal/output"
	"github.com/weslo/upool/internal/unity/fake"
)

// The tests of doctor's Editor and licence checks (docs/design.md, "Editors: budget by RAM,
// serialize cold imports as a courtesy, target by variable and by path": crash recovery, and the
// licence reported, never enforced), on the harness pool with the fake unity and an editorProcs
// for the process table.

// crashedPool is a harness pool whose ledger charges an Editor in slot-1 under closedPID, whose
// slot-1 has the Temp/UnityLockfile a crashed Editor leaves, and whose fake unity on PATH answers
// doctor with version, then replies, then the licence and the vcs check's replies.
func crashedPool(t *testing.T, replies ...fake.Reply) (harnessPool, *fake.Unity) {
	t.Helper()
	p := newHarnessPool(t)
	p.WriteFile(".claude/worktrees/slot-1/Temp/UnityLockfile", "")
	writeLedger(t, p, slot1Editor(p, true))
	script := append([]fake.Reply{capturedVersion(t, "unity", "version.raw.txt")}, replies...)
	script = append(script, licenceReply, vcsDoctorReply(p.Env))
	return p, p.installUnity(t, script...)
}

// runDoctor runs doctor --json in p's main checkout with procs as the process table, and returns
// the exit code and the editors lines.
func runDoctor(t *testing.T, p harnessPool, procs *editorProcs, args ...string) (int,
	[]doctorCheck) {
	t.Helper()
	r := runProcs(t, p.Env, p.Repo, procs, func() time.Time { return clock },
		append([]string{"doctor", "--json"}, args...)...)
	var editors []doctorCheck
	for _, c := range doctorChecks(t, r) {
		if c.Name == checkLedgerEditors {
			editors = append(editors, c)
		}
	}
	return r.code, editors
}

// tempExists reports whether slot-1's Temp/ is still there.
func tempExists(t *testing.T, p harnessPool) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(p.slot("slot-1"), "Temp"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return err == nil
}

// The acceptance test of #93: an Editor in the ledger whose process is gone, with no instance in
// `unity status` for its slot (testdata/captures/unity/status-no-instances.json, exit 6), has its
// entry and its slot's Temp/ removed under --fix. doctor asked `unity status` with the variable
// set to the slot.
func TestDoctorEditorsClearsACrashedEditor(t *testing.T) {
	p, u := crashedPool(t, fake.Reply{Capture: "status-no-instances.json", Exit: 6})
	code, got := runDoctor(t, p, procsAlive(), "--fix")
	want := []doctorCheck{{checkLedgerEditors, checkPass, "fixed: removed slot-1's Editor, " +
		"process 10648, which was gone, and its Temp/, since unity status lists no Editor " +
		"with it open"}}
	if code != output.ExitOK || !slices.Equal(got, want) {
		t.Errorf("exit %d, editors %+v; want 0 and %+v", code, got, want)
	}
	if tempExists(t, p) {
		t.Error("slot-1's Temp/ is still there")
	}
	checkEditors(t, p)
	i := slices.IndexFunc(u.Calls(), func(c fake.Call) bool { return c.Args[0] == "status" })
	if i < 0 || !slices.Equal(u.Calls()[i].Args, []string{"status", "--format", "json"}) ||
		u.Calls()[i].ProjectPath != p.slot("slot-1") {
		t.Errorf("unity calls = %+v, want `status --format json` targeting slot-1", u.Calls())
	}
}

// The acceptance test of #93, an instance listed: with `unity status` listing an Editor in the
// slot (testdata/captures/unity/status-one-editor.json), the dead entry still goes, but Temp/
// stays, since an Editor may have the project open, and the line warns why.
func TestDoctorEditorsKeepsTempWhileAnEditorIsListed(t *testing.T) {
	p, _ := crashedPool(t, fake.Reply{Capture: "status-one-editor.json", Vars: spikeVars})
	code, got := runDoctor(t, p, procsAlive(), "--fix")
	if code != output.ExitOK || len(got) != 1 || got[0].Result != checkWarn ||
		!strings.HasSuffix(got[0].Reason, "kept its Temp/, since unity status lists an Editor "+
			"with it open") {
		t.Errorf("exit %d, editors %+v; want 0 and a warning that Temp/ was kept", code, got)
	}
	if !tempExists(t, p) {
		t.Error("slot-1's Temp/ was deleted while an Editor is listed")
	}
	checkEditors(t, p)
}

// Without --fix, doctor reports the dead Editor, fails, and changes nothing: no `unity status`
// call, the entry and Temp/ kept.
func TestDoctorEditorsReportsADeadEditor(t *testing.T) {
	p, u := crashedPool(t)
	code, got := runDoctor(t, p, procsAlive())
	want := []doctorCheck{{checkLedgerEditors, checkFail, "slot-1's Editor, process 10648, is " +
		"gone, but the ledger still charges it; upool doctor --fix removes it, and its Temp/ " +
		"when no Editor has the slot open"}}
	if code != output.ExitFailure || !slices.Equal(got, want) {
		t.Errorf("exit %d, editors %+v; want 1 and %+v", code, got, want)
	}
	if slices.ContainsFunc(u.Calls(), func(c fake.Call) bool { return c.Args[0] == "status" }) {
		t.Errorf("unity calls = %+v, want no status call without --fix", u.Calls())
	}
	if !tempExists(t, p) {
		t.Error("slot-1's Temp/ was deleted without --fix")
	}
	checkEditors(t, p, slot1Editor(p, true))
}

// A running Editor passes, and --fix leaves it and its Temp/ alone.
func TestDoctorEditorsPassesARunningEditor(t *testing.T) {
	p, _ := crashedPool(t)
	code, got := runDoctor(t, p, procsAlive(closedPID), "--fix")
	want := []doctorCheck{{checkLedgerEditors, checkPass,
		"every Editor the ledger charges in this pool is running (1)"}}
	if code != output.ExitOK || !slices.Equal(got, want) {
		t.Errorf("exit %d, editors %+v; want 0 and %+v", code, got, want)
	}
	if !tempExists(t, p) {
		t.Error("a running Editor's Temp/ was deleted")
	}
	checkEditors(t, p, slot1Editor(p, true))
}

// doctor reports the licence as information: an active one passes, with what it holds
// (testdata/captures/unity/license-status.json); an inactive one warns, and neither fails doctor.
func TestDoctorReportsTheLicence(t *testing.T) {
	for _, tt := range []struct {
		name    string
		replace []string
		want    doctorCheck
	}{
		{"active", nil, doctorCheck{checkLicence, checkPass,
			"active, signed in (oauth), holding Unity Personal, Asset Store"}},
		{"inactive", []string{`"active": true`, `"active": false`}, doctorCheck{checkLicence,
			checkWarn, "not active, signed in (oauth), holding Unity Personal, Asset Store; " +
				"upool enforces nothing about it"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := newHarnessPool(t)
			licence := licenceReply
			licence.Replace = tt.replace
			u := p.installUnity(t, capturedVersion(t, "unity", "version.raw.txt"), licence,
				vcsDoctorReply(p.Env))
			r := runProcs(t, p.Env, p.Repo, procsAlive(), func() time.Time { return clock },
				"doctor", "--json")
			got := findCheck(t, doctorChecks(t, r), checkLicence)
			if r.code != output.ExitOK || got != tt.want {
				t.Errorf("exit %d, licence %+v; want 0 and %+v", r.code, got, tt.want)
			}
			if c := u.Calls()[1]; !slices.Equal(c.Args, []string{"license", "status",
				"--format", "json"}) || c.Dir != p.Repo {
				t.Errorf("unity %v in %s, want `license status` in the main checkout", c.Args,
					c.Dir)
			}
		})
	}
}
