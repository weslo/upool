// Package fake is the fake `unity` that tests run in place of the Unity CLI. It is the test binary
// itself: a package's TestMain hands over to Main, which acts as the CLI when the binary runs as
// `unity`, and Install puts a `unity` symlink to the binary first on a PATH that the test hands to
// the code under test (CONTRIBUTING.md, "Testing").
//
// The fake records and replays; it does not imitate Unity. Each call is logged with its arguments,
// UNITY_PROJECT_PATH and working directory, then answered with the next scripted capture from
// testdata/captures/unity/ and its exit code. It never chooses an Editor: targeting rules
// (docs/contracts.md (c)) are asserted on the recorded calls, on what upool sent.
//
// InstallClaude makes the same fake answer as `claude`, from testdata/captures/claude/, for the one
// Claude Code command upool runs itself: `claude --version`, which doctor checks (#57).
package fake

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/weslo/upool/internal/pathvars"
)

// The commands the fake answers to: the names of the symlinks Install and InstallClaude make, and
// of the directories under testdata/captures/ their replies come from.
const (
	unityName  = "unity"
	claudeName = "claude"
)

// The files Install writes beside the symlink, where the fake finds them.
const (
	scriptFile = "script.json"
	logFile    = "calls.jsonl"
)

// Reply is one scripted answer: what a call prints and how it exits.
type Reply struct {
	// Capture is a file under testdata/captures/unity/, or testdata/captures/claude/ for
	// InstallClaude, such as "close-gui.json". Once its variables are filled, it is written to
	// stdout byte for byte.
	Capture string
	// Vars fill the capture's path variables (testdata/README.md) with the test's own paths.
	Vars []pathvars.Var
	// Replace, when not empty, makes the reply a copy of the capture with each old string replaced
	// by its new one, given in pairs as strings.NewReplacer takes them: a value no CLI printed,
	// such as a version below upool's minimum. The capture itself is never changed.
	Replace []string
	// Exit is the exit code: the one docs/contracts.md (d) gives for the capture.
	Exit int
	// Stderr, when not empty, is written to stderr.
	Stderr string
}

// Call is one recorded call of the fake.
type Call struct {
	// Args are the arguments after `unity`.
	Args []string
	// ProjectPath is UNITY_PROJECT_PATH, and ProjectPathSet whether it was set at all.
	ProjectPath    string
	ProjectPathSet bool
	// Dir is the working directory with symlinks resolved, as tests resolve their own temporary
	// paths (CONTRIBUTING.md, "Isolated from this machine").
	Dir string
}

// Unity is an installed fake: a fake Unity CLI, or from InstallClaude a fake Claude Code CLI.
type Unity struct {
	// PATH is the PATH to hand to the code under test: the fake's directory, then this process's
	// PATH, so that git and the rest are still found.
	PATH string

	t   testing.TB
	dir string
}

// Dir is the directory holding the fake's symlink, to put on a PATH of the test's own making.
func (u *Unity) Dir() string { return u.dir }

// answer is a Reply as the fake replays it, with its capture read and filled.
type answer struct {
	Stdout []byte
	Stderr []byte
	Exit   int
}

// Install makes a fake `unity` that answers its calls with replies, in order: a temporary
// directory holding a `unity` symlink to this test binary, the script and the log. A call beyond
// the script exits non-zero and fails the test when it ends.
func Install(t testing.TB, replies ...Reply) *Unity {
	t.Helper()
	return install(t, unityName, replies)
}

// InstallClaude makes a fake `claude`, as Install makes a fake `unity`, answering with captures
// from testdata/captures/claude/.
func InstallClaude(t testing.TB, replies ...Reply) *Unity {
	t.Helper()
	return install(t, claudeName, replies)
}

// install makes a fake that answers as name.
func install(t testing.TB, name string, replies []Reply) *Unity {
	t.Helper()
	dir := t.TempDir()
	// A relative os.Args[0] would resolve against the symlink's directory, not the cwd.
	self, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(self, filepath.Join(dir, name)); err != nil {
		t.Fatal(err)
	}
	script := make([]answer, 0, len(replies))
	for _, r := range replies {
		raw, err := os.ReadFile(filepath.Join(capturesDir(name), r.Capture))
		if err != nil {
			t.Fatal(err)
		}
		filled, err := pathvars.Fill(string(raw), r.Vars...)
		if err != nil {
			t.Fatalf("filling %s: %v", r.Capture, err)
		}
		if len(r.Replace) > 0 {
			filled = strings.NewReplacer(r.Replace...).Replace(filled)
		}
		a := answer{Stdout: []byte(filled), Stderr: []byte(r.Stderr), Exit: r.Exit}
		script = append(script, a)
	}
	data, err := json.Marshal(script)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, scriptFile), data, 0o600); err != nil {
		t.Fatal(err)
	}

	u := &Unity{PATH: dir, t: t, dir: dir}
	if path := os.Getenv("PATH"); path != "" {
		u.PATH += string(os.PathListSeparator) + path
	}
	t.Cleanup(func() {
		calls, err := readLog(filepath.Join(dir, logFile))
		if err != nil {
			t.Error(err)
			return
		}
		if len(calls) > len(script) {
			t.Errorf("%s called %d times, want at most %d, the script's replies; "+
				"calls beyond it: %+v", name, len(calls), len(script), calls[len(script):])
		}
	})
	return u
}

// Calls returns the calls made so far, in the order they took their replies.
func (u *Unity) Calls() []Call {
	u.t.Helper()
	calls, err := readLog(filepath.Join(u.dir, logFile))
	if err != nil {
		u.t.Fatal(err)
	}
	return calls
}

// capturesDir is testdata/captures/<name>/, found from this file's path so that it does not
// depend on which package's tests are running.
func capturesDir(name string) string {
	_, file, _, _ := runtime.Caller(0) // without it, the read that follows names the missing file
	return filepath.Join(filepath.Dir(file), "..", "..", "..", "testdata", "captures", name)
}

// readLog reads the calls the log at path records; a missing log means no calls yet.
func readLog(path string) ([]Call, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }() // read-only, so closing loses nothing
	var calls []Call
	dec := json.NewDecoder(f)
	for {
		var c Call
		err := dec.Decode(&c)
		if errors.Is(err, io.EOF) {
			return calls, nil
		}
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", path, err)
		}
		calls = append(calls, c)
	}
}
