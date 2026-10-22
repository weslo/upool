package main

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/weslo/upool/internal/output"
)

// After a real init, which clones slot-1 with cp -cR and registers the hooks in the project,
// every check passes, the clone-sharing check among them, and its scratch files are gone; only the
// vcs lines warn, reporting the findings of the capture the fake unity replays.
func TestDoctorPassesThePoolInitMakes(t *testing.T) {
	e := newInitEnv(t)
	mustInit(t, e)
	unity, claude := capturedTools(t)
	fakeTools(t, e, unity, claude)
	r := runIn(t, e, e.Repo, "doctor", "--json")
	if r.code != output.ExitOK {
		t.Fatalf("exit = %d, want %d; stdout %s", r.code, output.ExitOK, r.stdout)
	}
	var names []string
	for _, c := range doctorChecks(t, r) {
		names = append(names, c.Name)
		want := checkPass
		if c.Name == checkVCS {
			want = checkWarn
		}
		if c.Result != want {
			t.Errorf("%s: %s, %q; want %s", c.Name, c.Result, c.Reason, want)
		}
		if c.Name == checkCloneSharing && !strings.HasPrefix(c.Reason,
			"APFS, one device, and a clone of a 128 MB file cost ") {
			t.Errorf("clone-sharing reason = %q, want what the clone cost", c.Reason)
		}
	}
	want := append([]string{checkGitignore, checkLibrary, "slot-1", checkCloneSharing,
		checkStateVolume, checkLeases, checkHookSettings, checkHookProvenance, "unity-cli",
		"claude-code", checkLedgerEditors, checkLicence},
		slices.Repeat([]string{checkVCS}, vcsLines(t))...)
	if strings.Join(names, " ") != strings.Join(want, " ") {
		t.Errorf("checks = %v, want %v", names, want)
	}
	entries, err := os.ReadDir(e.Path(".claude/worktrees"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "slot-1" {
		t.Errorf(".claude/worktrees holds %v, want slot-1 alone", entries)
	}
}
