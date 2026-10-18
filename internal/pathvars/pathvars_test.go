package pathvars

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Test paths live under /Volumes so that this file passes `make check-paths`.
var (
	home  = Var{"home", "/Volumes/Work/alice"}
	repo  = Var{"repo", "/Volumes/Work/alice/Documents/Games/Down.Game"}
	spike = Var{"spike", "/Volumes/Work/alice/Documents/Games/.upool-spike"}
	tmp   = Var{"claude_tmp", "/Volumes/Tmp/claude-501"}
	cwd   = Var{"cwd", "/Volumes/Tmp/elsewhere"}
	all   = []Var{home, repo, spike, tmp, cwd}
)

func TestKeyReplacesSlashesAndDots(t *testing.T) {
	if got, want := Key(repo.Path), "-Volumes-Work-alice-Documents-Games-Down-Game"; got != want {
		t.Errorf("Key = %q, want %q", got, want)
	}
}

func TestFillReplacesEveryForm(t *testing.T) {
	got, err := Fill(`{"$schema": "${repo}/x", "key": "${repo_key}--y", "lc": "${repo_lc}"}`, repo)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"$schema": "/Volumes/Work/alice/Documents/Games/Down.Game/x", "key": "-Volumes-Work-alice-Documents-Games-Down-Game--y", "lc": "/volumes/work/alice/documents/games/down.game"}`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestFillFailsOnUnfilledVariable(t *testing.T) {
	_, err := Fill("${repo} ${home}/a ${home}/b", repo)
	if err == nil || !strings.Contains(err.Error(), "${home}") || strings.Count(err.Error(), "${home}") != 1 {
		t.Errorf("err = %v, want one mention of ${home}", err)
	}
}

func TestScrubPrefersLongestMatch(t *testing.T) {
	text := strings.Join([]string{
		repo.Path + "/.claude/worktrees/slot-1",
		home.Path + "/.claude/projects",
		Key(repo.Path) + "--claude-worktrees-slot-1",
		Key(home.Path) + "--upool-probe",
		strings.ToLower(repo.Path),
	}, "\n")
	got, err := Scrub(text, all...)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"${repo}/.claude/worktrees/slot-1",
		"${home}/.claude/projects",
		"${repo_key}--claude-worktrees-slot-1",
		"${home_key}--upool-probe",
		"${repo_lc}",
	}, "\n")
	if got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}

func TestScrubEarlierVarWinsATie(t *testing.T) {
	got, err := Scrub(repo.Path+"/Assets", repo, Var{"cwd", repo.Path})
	if err != nil {
		t.Fatal(err)
	}
	if want := "${repo}/Assets"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestScrubFailsOnLeftoverMachinePath(t *testing.T) {
	// Joined at run time so this file passes `make check-paths`.
	other := filepath.Join("/", "Users", "bob", "Library")
	_, err := Scrub(repo.Path+" "+other, repo)
	if err == nil || !strings.Contains(err.Error(), filepath.Join("/", "Users", "bob")) {
		t.Errorf("err = %v, want it to name the leftover path", err)
	}
}

// Scrub must undo Fill on every committed capture, so a capture taken with scripts/capture-unity.sh
// or scripts/capture-hook.sh comes out in the same form as the spike's.
func TestScrubUndoesFillOnEveryCapture(t *testing.T) {
	root := filepath.Join("..", "..", "testdata", "captures")
	n := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		n++
		t.Run(strings.TrimPrefix(path, root+string(filepath.Separator)), func(t *testing.T) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			filled, err := Fill(string(raw), all...)
			if err != nil {
				t.Fatal(err)
			}
			scrubbed, err := Scrub(filled, all...)
			if err != nil {
				t.Fatal(err)
			}
			if scrubbed != string(raw) {
				t.Errorf("scrub(fill(capture)) differs from the capture:\n%s", scrubbed)
			}
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatalf("no captures found under %s", root)
	}
}
