package main

import (
	"os"
	"strings"
	"testing"

	"github.com/weslo/upool/internal/output"
)

// After a real init, which clones slot-1 with cp -cR, every check passes, the clone-sharing check
// among them, and its scratch files are gone.
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
		if c.Result != checkPass {
			t.Errorf("%s: %s, %q; want pass", c.Name, c.Result, c.Reason)
		}
		if c.Name == checkCloneSharing && !strings.HasPrefix(c.Reason,
			"APFS, one device, and a clone of a 128 MB file cost ") {
			t.Errorf("clone-sharing reason = %q, want what the clone cost", c.Reason)
		}
	}
	want := []string{checkGitignore, checkLibrary, "slot-1", checkCloneSharing, checkStateVolume,
		checkLeases, "unity-cli", "claude-code"}
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
