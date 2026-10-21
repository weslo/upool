package unity

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/weslo/upool/internal/output"
	"github.com/weslo/upool/internal/pathvars"
	"github.com/weslo/upool/internal/unity/fake"
)

// contractRow is one row of docs/contracts.md (c)'s targeting table: the verbs its first cell
// names and whether its project-argument cell puts the project positionally.
type contractRow struct {
	verbs      []string
	positional bool
}

// backticked matches a `verb` in a table cell.
var backticked = regexp.MustCompile("`([^`]+)`")

// contractC reads the targeting table of docs/contracts.md (c), so that the verb table is tested
// against the contract as written rather than a copy of it.
func contractC(t *testing.T) []contractRow {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "contracts.md"))
	if err != nil {
		t.Fatal(err)
	}
	_, section, ok := strings.Cut(string(raw), "## (c) Unity CLI targeting")
	if !ok {
		t.Fatal("docs/contracts.md has no section (c)")
	}
	section, _, _ = strings.Cut(section, "Resolution without the variable")
	var rows []contractRow
	for _, line := range strings.Split(section, "\n") {
		if !strings.HasPrefix(line, "| `") {
			continue // the header, the separator and the prose
		}
		cells := strings.Split(line, "|")
		if len(cells) < 4 {
			t.Fatalf("row %q has fewer than three cells", line)
		}
		// The command cell can add subcommands in parentheses: `command` (list, `eval`, …).
		names, _, _ := strings.Cut(cells[1], " (")
		var row contractRow
		for _, m := range backticked.FindAllStringSubmatch(names, -1) {
			row.verbs = append(row.verbs, m[1])
		}
		row.positional = strings.Contains(cells[3], "positional")
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		t.Fatal("docs/contracts.md (c) has no rows")
	}
	return rows
}

// replies are the captures the fake answers each verb with. The test reads only what upool sent,
// never the reply, so a verb no one has captured (list, job, mcp, build) gets another capture: the
// fake records and replays, it does not imitate Unity (CONTRIBUTING.md, "Testing").
var replies = map[string]fake.Reply{
	"command":         {Capture: "command-save-all.json"},
	"status":          {Capture: "status-one-editor.json"},
	"recompile":       {Capture: "recompile-up-to-date.json"},
	"open":            {Capture: "open-gui.json"},
	"test":            {Capture: "test-positional.json"},
	"run":             {Capture: "run-not-a-unity-project.json", Exit: 6},
	"close":           {Capture: "close-headless.json"},
	"cache key":       {Capture: "cache-key.json"},
	"projects verify": {Capture: "projects-verify-pass.json"},
	"vcs doctor":      {Capture: "vcs-doctor.json", Exit: 4},
}

// slotDir is a slot for the calls to target: a directory with symlinks resolved, as the fake
// records its working directory.
func slotDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	slot := filepath.Join(dir, "project", ".claude", "worktrees", "slot-1")
	if err := os.MkdirAll(slot, 0o755); err != nil {
		t.Fatal(err)
	}
	return slot
}

// The acceptance test of #82: one subtest per row of docs/contracts.md (c), each calling every verb
// the row names through the table and the fake unity, and asserting on the recorded call that it
// carried UNITY_PROJECT_PATH=<slot>, ran in the slot, put the slot positionally exactly where the
// row says, and never passed --project-path. The table holds exactly (c)'s verbs.
func TestVerbTableMatchesContractC(t *testing.T) {
	byName := map[string]Verb{}
	for _, v := range Verbs {
		byName[v.Name()] = v
	}
	seen := map[string]bool{}
	for _, row := range contractC(t) {
		t.Run(strings.Join(row.verbs, ","), func(t *testing.T) {
			for _, name := range row.verbs {
				seen[name] = true
				v, ok := byName[name]
				if !ok {
					t.Errorf("(c) has unity %s, which the verb table lacks", name)
					continue
				}
				if v.Positional != row.positional {
					t.Errorf("unity %s: positional %t, (c) says %t", name, v.Positional,
						row.positional)
				}
				checkCall(t, v)
			}
		})
	}
	for _, v := range Verbs {
		if !seen[v.Name()] {
			t.Errorf("the verb table has unity %s, which (c) does not", v.Name())
		}
	}
}

// checkCall calls v through the fake unity and fails the test unless the recorded call followed
// (c)'s rule for it.
func checkCall(t *testing.T, v Verb) {
	t.Helper()
	slot := slotDir(t)
	reply, ok := replies[v.Name()]
	if !ok {
		reply = fake.Reply{Capture: "recompile-up-to-date.json"}
	}
	repo := filepath.Dir(filepath.Dir(filepath.Dir(slot)))
	reply.Vars = []pathvars.Var{{Name: "spike", Path: filepath.Dir(slot)},
		{Name: "cwd", Path: slot}, {Name: "repo", Path: repo}}
	u := fake.Install(t, reply)
	env := []string{"PATH=" + u.Dir(), "HOME=" + u.Dir(), ProjectPathVar + "=/somewhere/else"}
	got, err := Client{Runner: Exec{}, Env: env}.Call(context.Background(), v, slot, "--extra")
	if err != nil {
		t.Fatalf("unity %s: %v", v.Name(), err)
	}
	if got.Response.Command == "" {
		t.Errorf("unity %s: reply %+v did not decode", v.Name(), got.Response)
	}
	calls := u.Calls()
	if len(calls) != 1 {
		t.Fatalf("unity %s: calls = %+v, want one", v.Name(), calls)
	}
	c := calls[0]
	if !c.ProjectPathSet || c.ProjectPath != slot {
		t.Errorf("unity %s: UNITY_PROJECT_PATH = %q (set %t), want %q", v.Name(), c.ProjectPath,
			c.ProjectPathSet, slot)
	}
	if c.Dir != slot {
		t.Errorf("unity %s ran in %s, want the slot %s", v.Name(), c.Dir, slot)
	}
	want := slices.Clone(v.Words)
	if v.Positional {
		want = append(want, slot)
	}
	want = append(want, "--extra", "--format", "json")
	if !slices.Equal(c.Args, want) {
		t.Errorf("unity %v, want unity %v", c.Args, want)
	}
	if slices.Contains(c.Args, "--project-path") {
		t.Errorf("unity %v passes --project-path", c.Args)
	}
}

// fakeRunner is a Runner that returns one recorded result, for the tests that stay in-process.
type fakeRunner struct {
	res Result
	err error
	env []string // the environment of the last call
}

func (f *fakeRunner) Run(_ context.Context, _ string, env []string, _ ...string) (Result, error) {
	f.env = env
	return f.res, f.err
}

// captured reads a capture's bytes, for a fakeRunner to reply with.
func captured(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(capturesDir, name))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// A failed reply decodes, with its exit code, for the caller to map with Reply.Err; the call itself
// is not an error.
func TestCallReturnsAFailedReply(t *testing.T) {
	r := &fakeRunner{res: Result{Stdout: captured(t, "test-not-a-unity-project.json"),
		ExitCode: 6}}
	got, err := Client{Runner: r}.Call(context.Background(), VerbTest, "/work/slot")
	if err != nil {
		t.Fatal(err)
	}
	if got.Response.Success || got.ExitCode != 6 || len(got.Response.Errors) != 1 ||
		got.Response.Errors[0].Code != "COMMAND_FAILED" {
		t.Errorf("reply = %+v, want the failure, exit 6, COMMAND_FAILED", got)
	}
}

// A reply that is not JSON is a failure carrying the raw stdout, whatever the exit: `unity
// --version`'s text, replayed through the fake as an answer to `status`, exiting 0, fails with
// COMMAND_FAILED and ExitFailure.
func TestCallFailsAReplyThatIsNotJSON(t *testing.T) {
	slot := slotDir(t)
	u := fake.Install(t, fake.Reply{Capture: "version.raw.txt"})
	env := []string{"PATH=" + u.Dir(), "HOME=" + u.Dir()}
	_, err := Client{Runner: Exec{}, Env: env}.Call(context.Background(), VerbStatus, slot)
	var f *output.Failure
	if !errors.As(err, &f) || f.Exit() != output.ExitFailure ||
		f.Code() != output.CodeCommandFailed || !strings.Contains(err.Error(), "exited 0") ||
		!strings.Contains(err.Error(), `stdout "1.0.0-beta.13\n"`) {
		t.Errorf("error %v, want COMMAND_FAILED exiting 1 with the version on stdout", err)
	}
}

// A reply that is not JSON exits as a failure (d) does not list: ExitNoTarget when the CLI exited
// 6, ExitFailure otherwise (A1; A3); its stderr comes with its stdout.
func TestCallFailsAReplyThatIsNotJSONByItsExit(t *testing.T) {
	for _, tt := range []struct{ exit, want int }{{2, output.ExitFailure},
		{cliExitNoTarget, output.ExitNoTarget}} {
		r := &fakeRunner{res: Result{Stdout: []byte("Warning: something\n"),
			Stderr: []byte("unity: crashed\n"), ExitCode: tt.exit}}
		_, err := Client{Runner: r}.Call(context.Background(), VerbStatus, "/work/slot")
		var f *output.Failure
		if !errors.As(err, &f) || f.Exit() != tt.want || !strings.Contains(err.Error(),
			fmt.Sprintf("unity status /work/slot exited %d with a reply that is not JSON: "+
				`stdout "Warning: something\n", stderr "unity: crashed\n"`, tt.exit)) {
			t.Errorf("exit %d: error %v, want both streams and exit %d", tt.exit, err, tt.want)
		}
	}
}

// A command that could not run is the runner's error, unchanged.
func TestCallPassesTheRunnersError(t *testing.T) {
	r := &fakeRunner{err: ErrNoUnity}
	if _, err := (Client{Runner: r}).Call(context.Background(), VerbStatus,
		"/work/slot"); !errors.Is(err, ErrNoUnity) {
		t.Errorf("err = %v, want ErrNoUnity", err)
	}
}

// The environment keeps every entry but UNITY_PROJECT_PATH, which is replaced by the slot.
func TestCallSetsTheProjectPathOnce(t *testing.T) {
	r := &fakeRunner{res: Result{Stdout: captured(t, "recompile-up-to-date.json")}}
	env := []string{"PATH=/bin", ProjectPathVar + "=/a", "HOME=/h", ProjectPathVar + "=/b"}
	if _, err := (Client{Runner: r, Env: env}).Call(context.Background(), VerbRecompile,
		"/work/slot"); err != nil {
		t.Fatal(err)
	}
	want := []string{"PATH=/bin", "HOME=/h", ProjectPathVar + "=/work/slot"}
	if !slices.Equal(r.env, want) {
		t.Errorf("env = %v, want %v", r.env, want)
	}
	if !slices.Equal(env, []string{"PATH=/bin", ProjectPathVar + "=/a", "HOME=/h",
		ProjectPathVar + "=/b"}) {
		t.Errorf("the caller's env changed to %v", env)
	}
}

// Exec finds unity on the environment's PATH, not the process's, and says so when it is missing;
// it refuses a relative directory.
func TestExecFindsUnityOnTheEnvironmentsPath(t *testing.T) {
	if _, err := (Exec{}).Run(context.Background(), "/", []string{"PATH=" + t.TempDir()},
		"status"); !errors.Is(err, ErrNoUnity) {
		t.Errorf("err = %v, want ErrNoUnity", err)
	}
	if _, err := (Exec{}).Run(context.Background(), "relative", nil, "status"); err == nil {
		t.Error("a relative directory ran")
	}
}
