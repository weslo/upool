package unity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/weslo/upool/internal/output"
	"github.com/weslo/upool/internal/pathvars"
	"github.com/weslo/upool/internal/unity/fake"
)

// contractDRow is one row of docs/contracts.md (d), "Codes upool must handle".
type contractDRow struct {
	// exit is the Exit cell: a number, or "not recorded".
	exit string
	// code is the errors[].code cell's code, or "" for "—".
	code string
	// when is the When cell.
	when string
	// captures are the files under testdata/captures/unity/ that the Fixture cell names, globs
	// included; the evidence/ files it also names are projections the fake cannot replay.
	captures []string
}

// contractD reads the table of docs/contracts.md (d), "Codes upool must handle", so that the
// mapping is tested against the contract as written rather than a copy of it.
func contractD(t *testing.T) []contractDRow {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "contracts.md"))
	if err != nil {
		t.Fatal(err)
	}
	_, section, ok := strings.Cut(string(raw), "### Codes upool must handle")
	if !ok {
		t.Fatal("docs/contracts.md (d) has no table of the codes upool must handle")
	}
	section, _, _ = strings.Cut(section, "### upool's own exit codes")
	var rows []contractDRow
	for _, line := range strings.Split(section, "\n") {
		cells := strings.Split(line, "|")
		if !strings.HasPrefix(line, "| ") || len(cells) < 8 {
			continue // the prose
		}
		exit := strings.TrimSpace(cells[1])
		if exit == "Exit" || strings.HasPrefix(exit, "-") {
			continue // the header and the separator
		}
		row := contractDRow{exit: exit, when: cells[4], captures: fixtures(t, cells[6])}
		if m := backticked.FindStringSubmatch(cells[2]); m != nil {
			row.code = m[1]
		}
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		t.Fatal("docs/contracts.md (d) has no codes upool must handle")
	}
	return rows
}

// fixtures are the captures under testdata/captures/unity/ that a Fixture cell names. A name with
// no directory is in the directory of the name before it, as the table writes
// "`unity/status-*.json`, `close-*.json`"; a glob is expanded.
func fixtures(t *testing.T, cell string) []string {
	t.Helper()
	var names []string
	dir := ""
	for _, m := range backticked.FindAllStringSubmatch(cell, -1) {
		name := m[1]
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		if d, base := filepath.Split(name); d != "" {
			dir, name = d, base
		}
		if dir != "unity/" {
			continue
		}
		matches, err := filepath.Glob(filepath.Join(capturesDir, name))
		if err != nil || len(matches) == 0 {
			t.Fatalf("(d) names unity/%s, which matches no capture", name)
		}
		for _, f := range matches {
			names = append(names, filepath.Base(f))
		}
	}
	return names
}

// filledReply is a capture as the fake replies with it in the slot at slot, its variables filled
// and its replacements made, decoded.
func filledReply(t *testing.T, name, slot string, replace []string) (Response, []pathvars.Var) {
	t.Helper()
	vars := []pathvars.Var{{Name: "spike", Path: filepath.Dir(slot)}, {Name: "cwd", Path: slot},
		{Name: "repo", Path: filepath.Dir(filepath.Dir(filepath.Dir(slot)))}}
	filled, err := pathvars.Fill(string(captured(t, name)), vars...)
	if err != nil {
		t.Fatalf("filling %s: %v", name, err)
	}
	if len(replace) > 0 {
		filled = strings.NewReplacer(replace...).Replace(filled)
	}
	var r Response
	if err := json.Unmarshal([]byte(filled), &r); err != nil {
		t.Fatalf("decoding %s: %v", name, err)
	}
	return r, vars
}

// verbFor is the verb a capture's name starts with, such as VerbCacheKey for
// cache-key-project-path-invalid.json.
func verbFor(t *testing.T, name string) Verb {
	t.Helper()
	var found Verb
	for _, v := range Verbs {
		if prefix := strings.Join(v.Words, "-") + "-"; strings.HasPrefix(name, prefix) &&
			len(v.Words) >= len(found.Words) {
			found = v
		}
	}
	if found.Words == nil {
		t.Fatalf("no verb names the capture %s", name)
	}
	return found
}

// replay calls the verb a capture is named after through the fake unity, which answers with the
// capture, its replacements made, and exit, and returns the reply and the error upool fails with
// for it.
func replay(t *testing.T, name string, exit int, replace ...string) (Response, error) {
	t.Helper()
	slot := slotDir(t)
	want, vars := filledReply(t, name, slot, replace)
	u := fake.Install(t, fake.Reply{Capture: name, Vars: vars, Replace: replace, Exit: exit})
	env := []string{"PATH=" + u.Dir(), "HOME=" + u.Dir()}
	reply, err := Client{Runner: Exec{}, Env: env}.Call(context.Background(), verbFor(t, name),
		slot)
	if err != nil {
		t.Fatalf("unity %s replying with %s: %v", verbFor(t, name).Name(), name, err)
	}
	return want, reply.Err()
}

// recordedExit is the exit code a capture's .exit file holds, if it has one.
func recordedExit(t *testing.T, name string) (int, bool) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(capturesDir, strings.TrimSuffix(name, ".json")+".exit"))
	if errors.Is(err, os.ErrNotExist) {
		return 0, false
	}
	if err != nil {
		t.Fatal(err)
	}
	exit, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("%s.exit: %v", name, err)
	}
	return exit, true
}

// checkFailure fails the test unless err is the failure upool reports for want, a reply that
// failed: the CLI's first code passed through, with its message, exiting exit, and wrapping
// ErrMayHaveRun exactly when mayHaveRun.
func checkFailure(t *testing.T, err error, want Response, exit int, mayHaveRun bool) {
	t.Helper()
	var f *output.Failure
	if !errors.As(err, &f) {
		t.Fatalf("error %v, want an output failure", err)
	}
	if f.Exit() != exit || string(f.Code()) != want.Errors[0].Code {
		t.Errorf("exit %d, code %s; want %d, %s", f.Exit(), f.Code(), exit, want.Errors[0].Code)
	}
	if !strings.Contains(err.Error(), want.Errors[0].Message) {
		t.Errorf("error %q does not carry the CLI's message %q", err, want.Errors[0].Message)
	}
	if errors.Is(err, ErrMayHaveRun) != mayHaveRun {
		t.Errorf("errors.Is(%q, ErrMayHaveRun) = %t, want %t", err, !mayHaveRun, mayHaveRun)
	}
}

// exitUnlisted is an exit no row of (d) records, beside the CLI's 7, which (d) never saw.
const exitUnlisted = 9

// The acceptance test of #83: one subtest per row of docs/contracts.md (d), "Codes upool must
// handle", each replaying its captures through the fake unity with the row's exit. A capture that
// succeeded maps to no error. A failure maps to an output failure with the CLI's code and message
// passed through (A1); it exits ExitNoTarget for a row whose exit is 6, and ExitFailure for any
// other (A3); and only the row the code "ran anyway" in wraps ErrMayHaveRun. A row with no
// recorded exit is replayed with several, and every capture also with an exit no row records,
// none of which may change the result (A2); a row with no capture replays a code no capture
// holds. Where a failed capture has a .exit file, it must agree with its row.
func TestErrorMapping(t *testing.T) {
	for i, row := range contractD(t) {
		code := row.code
		if code == "" {
			code = "none"
		}
		name := fmt.Sprintf("row%d-exit-%s-%s", i+1, strings.ReplaceAll(row.exit, " ", "-"), code)
		t.Run(name, func(t *testing.T) {
			wantExit := output.ExitFailure
			if row.exit == strconv.Itoa(cliExitNoTarget) {
				wantExit = output.ExitNoTarget
			}
			mayHaveRun := strings.Contains(row.when, "ran anyway")
			exits := []int{0, 1, cliExitNoTarget} // for a row whose exit is not recorded
			if n, err := strconv.Atoi(row.exit); err == nil {
				exits = []int{n}
			}
			if len(row.captures) == 0 {
				if row.exit == "0" {
					t.Fatal("the success row names no capture")
				}
				for _, exit := range exits {
					want, err := replay(t, "status-no-instances.json", exit,
						codeStatusNoInstances, "UNRECORDED_CODE")
					checkFailure(t, err, want, wantExit, mayHaveRun)
				}
				return
			}
			failures := 0
			for _, capture := range row.captures {
				replayed := exits
				recorded, hasExit := recordedExit(t, capture)
				if hasExit {
					replayed = []int{recorded}
				}
				// An exit no row records, which must not change the result (A2).
				replayed = append(slices.Clone(replayed), exitUnlisted)
				failed := false
				for _, exit := range replayed {
					want, err := replay(t, capture, exit)
					failed = !want.Success
					switch {
					case want.Success:
						if err != nil {
							t.Errorf("%s, exit %d: error %v, want none", capture, exit, err)
						}
					case row.exit == "0":
						// A failure the glob matched, which another row covers.
					default:
						failures++
						if want.Errors[0].Code != row.code {
							t.Errorf("%s has code %s, (d) says %s", capture,
								want.Errors[0].Code, row.code)
						}
						checkFailure(t, err, want, wantExit, mayHaveRun)
					}
				}
				if hasExit && failed && row.exit != "0" && strconv.Itoa(recorded) != row.exit {
					t.Errorf("%s.exit records %d, (d) says %s", strings.TrimSuffix(capture,
						".json"), recorded, row.exit)
				}
			}
			if row.exit != "0" && failures == 0 {
				t.Errorf("no capture of the row failed: %v", row.captures)
			}
		})
	}
}

// A failure (d) lists is read by its code and message, whatever the exit (A2): the code decides
// both that the reply failed and how upool exits.
func TestErrorMappingReadsTheCodeNotTheExit(t *testing.T) {
	want, err := replay(t, "status-no-instances.json", 1)
	checkFailure(t, err, want, output.ExitNoTarget, false)
	want, err = replay(t, "cache-key-project-path-rejected.json", cliExitNoTarget)
	checkFailure(t, err, want, output.ExitFailure, false)
	if _, err := replay(t, "status-one-editor.json", 1); err != nil {
		t.Errorf("a reply that succeeded, exiting 1: error %v, want none", err)
	}
	want, err = replay(t, "command-eval-main-thread-timeout.json", 0)
	checkFailure(t, err, want, output.ExitFailure, true)
}

// A failure (d) does not list exits as the CLI did: ExitNoTarget for the CLI's 6, ExitFailure for
// any other (A1; A3). That holds for a known code with a message (d) does not record.
func TestErrorMappingFallsBackOnTheExit(t *testing.T) {
	unrecorded := []string{codeStatusNoInstances, "UNRECORDED_CODE"}
	unlisted := []string{"No Pipeline instance found for project", "Something else went wrong at"}
	for _, tt := range []struct {
		capture string
		replace []string
		exit    int
		want    int
	}{
		{"status-no-instances.json", unrecorded, cliExitNoTarget, output.ExitNoTarget},
		{"status-no-instances.json", unrecorded, 2, output.ExitFailure},
		{"command-eval-no-pipeline-instance.json", unlisted, cliExitNoTarget, output.ExitNoTarget},
		{"command-eval-no-pipeline-instance.json", unlisted, 1, output.ExitFailure},
	} {
		want, err := replay(t, tt.capture, tt.exit, tt.replace...)
		checkFailure(t, err, want, tt.want, false)
	}
}

// Every entry of a reply with several errors reaches the message, and the first gives the code. A
// failed reply with no errors at all is COMMAND_FAILED, exiting as the CLI did.
func TestErrorMappingKeepsEveryEntry(t *testing.T) {
	r := &fakeRunner{res: Result{Stdout: []byte(`{"success": false, "command": "status",
		"errors": [{"code": "AMBIGUOUS_EDITOR", "message": "first"},
		{"code": "LATER_CODE", "message": "second"}]}`), ExitCode: 6}}
	reply, err := Client{Runner: r}.Call(context.Background(), VerbStatus, "/work/slot")
	if err != nil {
		t.Fatal(err)
	}
	var f *output.Failure
	if err := reply.Err(); !errors.As(err, &f) || f.Code() != codeAmbiguousEditor ||
		f.Exit() != output.ExitNoTarget || err.Error() != "unity status /work/slot: "+
		"AMBIGUOUS_EDITOR: first; LATER_CODE: second" {
		t.Errorf("error %v, want both entries, AMBIGUOUS_EDITOR's code and exit 6", err)
	}

	for _, exit := range []int{0, cliExitNoTarget} {
		r = &fakeRunner{res: Result{Stdout: []byte(`{"success": false, "command": "status"}`),
			ExitCode: exit}}
		reply, err = Client{Runner: r}.Call(context.Background(), VerbStatus, "/work/slot")
		if err != nil {
			t.Fatal(err)
		}
		want := output.ExitFailure
		if exit == cliExitNoTarget {
			want = output.ExitNoTarget
		}
		if err := reply.Err(); !errors.As(err, &f) || f.Code() != output.CodeCommandFailed ||
			f.Exit() != want {
			t.Errorf("exit %d with no errors: error %v, want COMMAND_FAILED exiting %d", exit,
				err, want)
		}
	}
}
