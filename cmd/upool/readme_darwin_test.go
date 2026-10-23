//go:build darwin

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/weslo/upool/internal/output"
	"github.com/weslo/upool/internal/pathvars"
	"github.com/weslo/upool/internal/testenv"
)

// The acceptance test of #105: the upool commands in README.md's quickstart block, run as written
// against a harness project under a temporary HOME with upool on PATH as a Homebrew install puts
// it, each exit 0, after a cd to the project, and leave what "How a session gets a slot" starts
// from: a pool with a ready slot-1, which the create hook hands a desktop session. Commands that
// are not upool's, the brew install, are not run; TestReadmeQuickstartInstallsTheReleasedFormula
// checks it. init clones with `cp -cR`, so this runs on macOS only.
func TestReadmeQuickstart(t *testing.T) {
	e := newInitEnv(t)
	dir, ran := "", 0
	for _, cmd := range readmeQuickstart(t) {
		switch cmd[0] {
		case "cd":
			if len(cmd) != 2 || cmd[1] != projectPlaceholder {
				t.Fatalf("README.md's quickstart runs %q, want cd %s", cmd, projectPlaceholder)
			}
			dir = e.Repo
		case upoolName:
			if dir == "" {
				t.Fatalf("README.md's quickstart runs %q before cd %s", cmd, projectPlaceholder)
			}
			r := runIn(t, e, dir, cmd[1:]...)
			if r.code != output.ExitOK {
				t.Fatalf("%s: exit = %d, want %d; stderr %q", strings.Join(cmd, " "), r.code,
					output.ExitOK, r.stderr)
			}
			ran++
		}
	}
	if ran == 0 {
		t.Fatal("README.md's quickstart runs no upool command")
	}
	checkPool(t, e, testenv.Base)

	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "captures", "hooks",
		"desktop-session-create.json"))
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := pathvars.Fill(string(b), pathvars.Var{Name: "repo", Path: e.Repo})
	if err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	d := dispatcher{commands: commandTable, hooks: hookTable, stdin: strings.NewReader(stdin),
		stdout: &out, stderr: &errOut, getenv: e.Getenv, environ: e.Environ,
		now: func() time.Time { return clock }, pid: os.Getpid()}
	if code := d.run([]string{"hook", "worktree-create"}); code != output.ExitOK {
		t.Fatalf("create hook: exit = %d, want %d; stderr %q", code, output.ExitOK,
			errOut.String())
	}
	if want := slotPath(e) + "\n"; out.String() != want {
		t.Errorf("create hook: stdout = %q, want exactly %q", out.String(), want)
	}
}
