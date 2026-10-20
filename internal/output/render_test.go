package output

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/weslo/upool/internal/pathvars"
)

var update = flag.Bool("update", false, "rewrite the testdata/golden files this package checks")

// repo is the main checkout the tests' paths are under, and the golden files' ${repo}.
var repo = pathvars.Var{Name: "repo", Path: "/src/down"}

// slot stands in for a command's data: a JSON object whose human form is its path. Each command's
// real data arrives with that command's issue.
type slot struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

func (s slot) Human() string { return s.Path }

func success() Result {
	data := slot{Name: "slot-1", Path: repo.Path + "/.claude/worktrees/slot-1"}
	return Result{Command: "which", Data: data}
}

func failure() Result {
	err := Fail(CodeNotInSlot, "%s/Assets is not inside a slot", repo.Path)
	return Result{Command: "which", Err: err}
}

func render(asJSON bool, r Result) (exit int, stdout, stderr string) {
	var out, errOut bytes.Buffer
	exit = Render(&out, &errOut, asJSON, r)
	return exit, out.String(), errOut.String()
}

// checkGolden compares got, with its paths scrubbed, against testdata/golden/<name>, after
// rewriting the file first under -update.
func checkGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "golden", name)
	got, err := pathvars.Scrub(got, repo)
	if err != nil {
		t.Fatal(err)
	}
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("output =\n%s\nwant %s:\n%s", got, path, want)
	}
}

func TestSuccessEnvelopeGolden(t *testing.T) {
	exit, stdout, stderr := render(true, success())
	if exit != ExitOK {
		t.Errorf("exit = %d, want %d", exit, ExitOK)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	checkGolden(t, "envelope-success.json", stdout)
}

func TestFailureEnvelopeGolden(t *testing.T) {
	exit, stdout, stderr := render(true, failure())
	if exit != ExitFailure {
		t.Errorf("exit = %d, want %d", exit, ExitFailure)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	checkGolden(t, "envelope-failure.json", stdout)
}

func TestHumanSuccessGolden(t *testing.T) {
	exit, stdout, stderr := render(false, success())
	if exit != ExitOK {
		t.Errorf("exit = %d, want %d", exit, ExitOK)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	checkGolden(t, "human-success.stdout", stdout)
}

func TestHumanFailureGolden(t *testing.T) {
	exit, stdout, stderr := render(false, failure())
	if exit != ExitFailure {
		t.Errorf("exit = %d, want %d", exit, ExitFailure)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	checkGolden(t, "human-failure.stderr", stderr)
}

func TestRenderReturnsTheErrorsExitCode(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"no error", nil, ExitOK},
		{"Fail", Fail(CodeNotInSlot, "m"), ExitFailure},
		{"NoTarget", NoTarget(CodeCommandFailed, "m"), ExitNoTarget},
		{"wrapped NoTarget", fmt.Errorf("opening x: %w", NoTarget(CodeCommandFailed, "m")), ExitNoTarget},
		{"Exited", Exited(3, "m"), 3},
		{"wrapped Exited", fmt.Errorf("running x: %w", Exited(137, "m")), 137},
		{"Exited 0", Exited(ExitOK, "m"), ExitFailure},
		{"Exited out of range", Exited(256, "m"), ExitFailure},
		{"plain error", errors.New("m"), ExitFailure},
		{"wrapped plain error", fmt.Errorf("reading x: %w", fs.ErrNotExist), ExitFailure},
	}
	for _, tt := range tests {
		for _, asJSON := range []bool{true, false} {
			exit, _, _ := render(asJSON, Result{Command: "which", Err: tt.err})
			if exit != tt.want {
				t.Errorf("%s, json %t: exit = %d, want %d", tt.name, asJSON, exit, tt.want)
			}
		}
	}
}

// decode reads an envelope Render wrote.
func decode(t *testing.T, stdout string) Envelope {
	t.Helper()
	var env Envelope
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("decoding %q: %v", stdout, err)
	}
	return env
}

func TestEnvelopeErrorCarriesTheCodeAndTheWholeMessage(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want Error
	}{
		{"Failure", Fail(CodeNotInSlot, "not in a slot"), Error{"NOT_IN_SLOT", "not in a slot"}},
		{
			"wrapped Failure",
			fmt.Errorf("opening slot-1: %w", NoTarget(CodeCommandFailed, "no Editor")),
			Error{"COMMAND_FAILED", "opening slot-1: no Editor"},
		},
		{"plain error", errors.New("disk full"), Error{"COMMAND_FAILED", "disk full"}},
	}
	for _, tt := range tests {
		_, stdout, _ := render(true, Result{Command: "which", Err: tt.err})
		env := decode(t, stdout)
		if env.Success {
			t.Errorf("%s: success = true, want false", tt.name)
		}
		if !slices.Equal(env.Errors, []Error{tt.want}) {
			t.Errorf("%s: errors = %+v, want [%+v]", tt.name, env.Errors, tt.want)
		}
	}
}

func TestErrorKeepsItsCauseForErrorsIs(t *testing.T) {
	err := Fail(CodeCommandFailed, "reading %s: %w", "state.json", fs.ErrNotExist)
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("errors.Is(%v, fs.ErrNotExist) = false, want true", err)
	}
	var e *Failure
	if !errors.As(fmt.Errorf("taking a: %w", err), &e) {
		t.Fatalf("errors.As(wrapped %v, *Failure) = false, want true", err)
	}
	if e.Exit() != ExitFailure || e.Code() != CodeCommandFailed {
		t.Errorf("exit, code = %d, %s, want %d, %s", e.Exit(), e.Code(), ExitFailure, CodeCommandFailed)
	}
}

func TestHumanWarningsAndErrorGoToStderr(t *testing.T) {
	r := success()
	r.Warnings = []string{"first", "second"}
	r.Err = NoTarget(CodeCommandFailed, "no Editor")
	exit, stdout, stderr := render(false, r)
	if exit != ExitNoTarget {
		t.Errorf("exit = %d, want %d", exit, ExitNoTarget)
	}
	if want := r.Data.(slot).Path + "\n"; stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
	if want := "upool: warning: first\nupool: warning: second\nupool: no Editor\n"; stderr != want {
		t.Errorf("stderr = %q, want %q", stderr, want)
	}
}

func TestJSONKeepsWarningsInTheEnvelope(t *testing.T) {
	r := success()
	r.Warnings = []string{"first", "second"}
	_, stdout, stderr := render(true, r)
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if env := decode(t, stdout); !env.Success || !slices.Equal(env.Warnings, r.Warnings) {
		t.Errorf("success, warnings = %t, %q, want true, %q", env.Success, env.Warnings, r.Warnings)
	}
}

func TestDataWithoutHumanFormPrintsItsJSON(t *testing.T) {
	_, stdout, _ := render(false, Result{Command: "status", Data: map[string]int{"slots": 2}})
	if want := "{\n  \"slots\": 2\n}\n"; stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
}

func TestNilDataPrintsNothing(t *testing.T) {
	exit, stdout, stderr := render(false, Result{Command: "release"})
	if exit != ExitOK || stdout != "" || stderr != "" {
		t.Errorf("exit, stdout, stderr = %d, %q, %q, want %d, empty, empty", exit, stdout, stderr, ExitOK)
	}
}

func TestEnvelopeWritesNullDataOnFailure(t *testing.T) {
	_, stdout, _ := render(true, failure())
	if !strings.Contains(stdout, `"data": null`) {
		t.Errorf("stdout = %s, want \"data\": null", stdout)
	}
}

// failingWriter fails every write, as stdout does once its reader has gone.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }

func TestFailedWriteFailsASuccess(t *testing.T) {
	for _, asJSON := range []bool{true, false} {
		var errOut bytes.Buffer
		exit := Render(failingWriter{}, &errOut, asJSON, success())
		if exit != ExitFailure {
			t.Errorf("json %t: exit = %d, want %d", asJSON, exit, ExitFailure)
		}
		if want := "upool: writing the result: broken pipe\n"; errOut.String() != want {
			t.Errorf("json %t: stderr = %q, want %q", asJSON, errOut.String(), want)
		}
	}
}

func TestFailedWriteKeepsTheFailuresExitCode(t *testing.T) {
	r := Result{Command: "open", Err: NoTarget(CodeCommandFailed, "no Editor")}
	if exit := Render(failingWriter{}, &bytes.Buffer{}, true, r); exit != ExitNoTarget {
		t.Errorf("exit = %d, want %d", exit, ExitNoTarget)
	}
}

func TestDataThatCannotBeEncodedFails(t *testing.T) {
	for _, asJSON := range []bool{true, false} {
		exit, stdout, stderr := render(asJSON, Result{Command: "status", Data: make(chan int)})
		if exit != ExitFailure {
			t.Errorf("json %t: exit = %d, want %d", asJSON, exit, ExitFailure)
		}
		if stdout != "" {
			t.Errorf("json %t: stdout = %q, want empty", asJSON, stdout)
		}
		if !strings.HasPrefix(stderr, "upool: encoding the result: ") {
			t.Errorf("json %t: stderr = %q, want an encoding error", asJSON, stderr)
		}
	}
}
