package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/weslo/upool/internal/config"
	"github.com/weslo/upool/internal/output"
	"github.com/weslo/upool/internal/pool"
	"github.com/weslo/upool/internal/testenv"
	"github.com/weslo/upool/internal/unity/fake"
)

// The tests of `upool doctor`, on the hand-made harness pool (harness_test.go), so that they run
// everywhere. The clone-sharing check runs only on macOS.

// doctorRuns is how many `--version` calls the fakes fakeTools installs answer: more doctor runs
// than any one test makes.
const doctorRuns = 8

// capturedVersion is a reply with the version capture file under testdata/captures/<tool>/ and
// the exit code its .exit file records.
func capturedVersion(t *testing.T, tool, file string) fake.Reply {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "captures", tool, "version.exit"))
	if err != nil {
		t.Fatal(err)
	}
	exit, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	return fake.Reply{Capture: file, Exit: exit}
}

// fakeTools installs a fake unity and a fake claude first on e's PATH, each answering doctorRuns
// calls with its reply, so that doctor never runs this machine's own.
func fakeTools(t *testing.T, e *testenv.Env, unity, claude fake.Reply) (u, c *fake.Unity) {
	t.Helper()
	u = fake.Install(t, slices.Repeat([]fake.Reply{unity}, doctorRuns)...)
	c = fake.InstallClaude(t, slices.Repeat([]fake.Reply{claude}, doctorRuns)...)
	e.Setenv("PATH", c.Dir()+string(os.PathListSeparator)+u.PATH)
	return u, c
}

// installTools puts a fake unity and claude on the harness pool's PATH, as fakeTools does.
func (p *harnessPool) installTools(t *testing.T, unity, claude fake.Reply) (u, c *fake.Unity) {
	t.Helper()
	p.tools = true
	return fakeTools(t, p.Env, unity, claude)
}

// capturedTools are the replies that print the captured versions.
func capturedTools(t *testing.T) (unity, claude fake.Reply) {
	t.Helper()
	return capturedVersion(t, "unity", "version.raw.txt"), capturedVersion(t, "claude", "version.txt")
}

// doctorChecks returns the checks a doctor run reported, from its --json envelope, and fails the
// test unless the envelope's success matches the exit code.
func doctorChecks(t *testing.T, r result) []doctorCheck {
	t.Helper()
	env := decode(t, r.stdout)
	if env.Success != (r.code == output.ExitOK) || env.Command != "doctor" {
		t.Fatalf("envelope = %+v with exit %d, want doctor's, succeeding exactly when it exits 0",
			env, r.code)
	}
	data, _ := json.Marshal(env.Data) // it was decoded from JSON, so it encodes
	var report doctorReport
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatal(err)
	}
	return report.Checks
}

// wantChecks are the checks a healthy harness pool reports, in order.
func wantChecks() []string {
	names := []string{checkGitignore, checkLibrary, "slot-1", "slot-2"}
	if runtime.GOOS == "darwin" {
		names = append(names, checkCloneSharing)
	}
	return append(names, checkStateVolume, checkLeases, checkHookSettings,
		checkHookProvenance, "unity-cli", "claude-code")
}

// checkResults fails the test unless checks are wantChecks, in order, each passing but the one
// named in odd, which has that result and a reason containing reason. Without p's tools on PATH
// the version checks warn instead.
func (p harnessPool) checkResults(t *testing.T, checks []doctorCheck, odd string,
	result checkResult, reason string) {
	t.Helper()
	var names []string
	for _, c := range checks {
		names = append(names, c.Name)
		want := checkPass
		if !p.tools && (c.Name == "unity-cli" || c.Name == "claude-code") {
			want = checkWarn
		}
		switch {
		case c.Name == odd && (c.Result != result || !strings.Contains(c.Reason, reason)):
			t.Errorf("%s: %s, %q; want %s, with a reason containing %q", c.Name, c.Result,
				c.Reason, result, reason)
		case c.Name != odd && c.Result != want:
			t.Errorf("%s: %s, %q; want %s", c.Name, c.Result, c.Reason, want)
		}
	}
	if want := wantChecks(); strings.Join(names, " ") != strings.Join(want, " ") {
		t.Errorf("checks = %v, want %v", names, want)
	}
}

// A healthy pool passes every check, from wherever in it doctor runs. The fake unity and claude go
// on PATH only for the last run, the human form, since each call of one is a run of this test
// binary; until then the version checks warn.
func TestDoctorPassesAHealthyPool(t *testing.T) {
	p := newHarnessPool(t)
	for _, dir := range []string{p.Repo, p.Path("Assets/Scripts"), p.slot("slot-2")} {
		r := runIn(t, p.Env, dir, "doctor", "--json")
		if r.code != output.ExitOK || r.stderr != "" {
			t.Errorf("from %s: exit = %d, stderr = %q, want 0 and empty", dir, r.code, r.stderr)
		}
		p.checkResults(t, doctorChecks(t, r), "", "", "")
	}

	unity, claude := capturedTools(t)
	p.installTools(t, unity, claude)
	r := runIn(t, p.Env, p.Repo, "doctor")
	lines := strings.Split(strings.TrimSuffix(r.stdout, "\n"), "\n")
	if lines[0] != p.Repo || len(lines) != 1+len(wantChecks()) {
		t.Fatalf("stdout =\n%s\nwant the main checkout, then a line per check", r.stdout)
	}
	for i, name := range wantChecks() {
		if f := strings.Fields(lines[i+1]); len(f) < 3 || f[0] != "pass" || f[1] != name {
			t.Errorf("line %d = %q, want pass, %s and its reason", i+2, lines[i+1], name)
		}
	}
}

// Each precondition the issue names fails its own check, and only that check; doctor then exits
// 1 and names it.
func TestDoctorFailsEachBrokenPrecondition(t *testing.T) {
	tests := []struct {
		name   string
		breaks func(t *testing.T, p harnessPool)
		check  string
		reason string
	}{
		{"a tracked Library", func(_ *testing.T, p harnessPool) {
			p.Git("add", "-f", "Library/ArtifactDB")
			p.Git("commit", "-q", "-m", "track the Library")
		}, checkLibrary, "such as Library/ArtifactDB: untrack Library/"},
		{"no .gitignore entry", func(t *testing.T, p harnessPool) {
			p.WriteFile(".gitignore", strings.Replace(readFile(t, p.Env, ".gitignore"),
				worktreesLine+"\n", "", 1))
		}, checkGitignore, "git does not ignore .claude/worktrees: add .claude/worktrees/ to " +
			".gitignore"},
		{"a slot removed with git worktree remove", func(_ *testing.T, p harnessPool) {
			p.Git("worktree", "remove", p.slot("slot-2"))
		}, "slot-2", "is not on disk; git worktree list does not list it"},
		{"a slot whose directory was deleted", func(t *testing.T, p harnessPool) {
			if err := os.RemoveAll(p.slot("slot-2")); err != nil {
				t.Fatal(err)
			}
		}, "slot-2", "is not on disk"},
		{"a slot whose .git points elsewhere", func(_ *testing.T, p harnessPool) {
			p.WriteFile(".claude/worktrees/slot-2/.git", "gitdir: "+p.Home+"\n")
		}, "slot-2", "points to " + "HOME, outside"},
		{"a slot that is a file", func(t *testing.T, p harnessPool) {
			p.Git("worktree", "remove", p.slot("slot-2"))
			p.WriteFile(".claude/worktrees/slot-2", "not a slot\n")
		}, "slot-2", "is not a directory; git worktree list does not list it"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newHarnessPool(t)
			tt.breaks(t, p)
			reason := strings.ReplaceAll(tt.reason, "HOME", p.Home)
			r := runIn(t, p.Env, p.Repo, "doctor", "--json")
			if r.code != output.ExitFailure {
				t.Errorf("exit = %d, want %d", r.code, output.ExitFailure)
			}
			env := decode(t, r.stdout)
			want := "1 of " + strconv.Itoa(len(wantChecks())) + " checks failed: " + tt.check
			if len(env.Errors) != 1 || env.Errors[0].Message != want ||
				env.Errors[0].Code != string(output.CodeCommandFailed) {
				t.Errorf("errors = %+v, want one COMMAND_FAILED, %q", env.Errors, want)
			}
			p.checkResults(t, doctorChecks(t, r), tt.check, checkFail, reason)

			r = runIn(t, p.Env, p.Repo, "doctor")
			if r.code != output.ExitFailure || r.stderr != "upool: "+want+"\n" ||
				!strings.Contains(r.stdout, "\nfail  ") {
				t.Errorf("exit = %d, stdout %q, stderr %q; want 1, a fail line, and %q",
					r.code, r.stdout, r.stderr, want)
			}
		})
	}
}

// A lease with no holder PID warns once it is older than agent_ttl, from the configuration; one
// with a holder PID, or a younger one, does not. A warning alone exits 0.
func TestDoctorWarnsAboutALeaseWithNoHolderPIDOlderThanAgentTTL(t *testing.T) {
	pid := 4242
	lease := func(holder string, age time.Duration, holderPID *int) *pool.Lease {
		return &pool.Lease{LeaseID: "id-" + holder, Holder: holder, HolderPID: holderPID,
			AcquiredAt: clock.Add(-age), Confirmed: true, Lane: "script"}
	}
	tests := []struct {
		name           string
		ttl            string
		lease1, lease2 *pool.Lease
		result         checkResult
		reason         string
	}{
		{"older than the default 24h", "", lease("alice", 25*time.Hour, nil),
			lease("bob", 72*time.Hour, &pid), checkWarn,
			"leased with no holder PID for longer than agent_ttl (1d): slot-1 (alice, 1d1h); " +
				"release each once its work is done"},
		{"younger than the default", "", lease("alice", 23*time.Hour, nil), nil, checkPass,
			"no lease without a holder PID is older than agent_ttl (1d)"},
		{"older than a configured agent_ttl", "1h", lease("", 90*time.Minute, nil),
			lease("bob", 2*time.Hour, nil), checkWarn, "agent_ttl (1h): slot-1 (no holder, " +
				"1h30m), slot-2 (bob, 2h)"},
		{"younger than a configured agent_ttl", "2d", lease("alice", 25*time.Hour, nil), nil,
			checkPass, "older than agent_ttl (2d)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newHarnessPool(t)
			if tt.ttl != "" {
				p.Setenv(config.EnvName("agent_ttl"), tt.ttl)
			}
			p.update(t, func(pl *pool.Pool) {
				pl.Slots[0].State, pl.Slots[0].Lease = pool.StateLeased, tt.lease1
				if tt.lease2 != nil {
					pl.Slots[1].State, pl.Slots[1].Lease = pool.StateLeased, tt.lease2
				}
			})
			r := runIn(t, p.Env, p.Repo, "doctor", "--json")
			if r.code != output.ExitOK {
				t.Errorf("exit = %d, want %d", r.code, output.ExitOK)
			}
			odd := ""
			if tt.result != checkPass {
				odd = checkLeases
			}
			checks := doctorChecks(t, r)
			p.checkResults(t, checks, odd, tt.result, tt.reason)
			if c := findCheck(t, checks, checkLeases); !strings.Contains(c.Reason, tt.reason) {
				t.Errorf("leases reason = %q, want it to contain %q", c.Reason, tt.reason)
			}
		})
	}
}

// ~/.upool inside a cloud-synced folder, here through a symlink, warns: the lock is advisory there.
func TestDoctorWarnsWhenStateIsInACloudSyncedFolder(t *testing.T) {
	e := newInitEnv(t)
	cloud := filepath.Join(e.Home, "Library", "CloudStorage", "Dropbox", "upool")
	if err := os.MkdirAll(cloud, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(cloud, filepath.Join(e.Home, ".upool")); err != nil {
		t.Fatal(err)
	}
	p := harnessPoolIn(t, e)
	r := runIn(t, p.Env, p.Repo, "doctor", "--json")
	if r.code != output.ExitOK {
		t.Errorf("exit = %d, want %d", r.code, output.ExitOK)
	}
	p.checkResults(t, doctorChecks(t, r), checkStateVolume, checkWarn,
		"~/.upool resolves to "+cloud+", inside ~/Library/CloudStorage, which a cloud service "+
			"syncs: flock is advisory there")
}

func TestDoctorOutsideAPoolFails(t *testing.T) {
	p := newHarnessPool(t)
	outside := filepath.Dir(p.Repo)
	r := runIn(t, p.Env, outside, "doctor")
	want := "upool: " + outside + " is not inside a registered pool; run upool doctor in a " +
		"pool's checkout or slot\n"
	if r.code != output.ExitFailure || r.stdout != "" || r.stderr != want {
		t.Errorf("exit = %d, stdout %q, stderr %q; want 1, nothing, and %q", r.code, r.stdout,
			r.stderr, want)
	}
}

func TestDoctorRefusesArguments(t *testing.T) {
	p := newHarnessPool(t)
	r := runIn(t, p.Env, p.Repo, "doctor", "slot-1")
	if r.code != output.ExitFailure || !strings.Contains(r.stderr,
		`unexpected arguments ["slot-1"]; usage: upool doctor [--fix] [--json]`) {
		t.Errorf("exit = %d, stderr %q; want 1 and the usage", r.code, r.stderr)
	}
}

// findCheck returns the check named name among checks, failing the test if there is none.
func findCheck(t *testing.T, checks []doctorCheck, name string) doctorCheck {
	t.Helper()
	for _, c := range checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("checks = %+v, want one named %s", checks, name)
	return doctorCheck{}
}

// A ready slot with no lease whose directory is gone is reported, and --fix drops it, pruning a
// registration git still has, so that a later clone replaces it ((f) transition 17).
func TestDoctorFixDropsAMissingReadySlot(t *testing.T) {
	tests := []struct {
		name   string
		remove func(t *testing.T, p harnessPool)
		reason string
	}{
		{"its directory deleted", func(t *testing.T, p harnessPool) {
			if err := os.RemoveAll(p.slot("slot-2")); err != nil {
				t.Fatal(err)
			}
		}, "SLOT is not on disk"},
		{"removed with git worktree remove", func(_ *testing.T, p harnessPool) {
			p.Git("worktree", "remove", p.slot("slot-2"))
		}, "SLOT is not on disk; git worktree list does not list it"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newHarnessPool(t)
			tt.remove(t, p)
			reason := strings.ReplaceAll(tt.reason, "SLOT", p.slot("slot-2"))

			r := runIn(t, p.Env, p.Repo, "doctor", "--json")
			if c := findCheck(t, doctorChecks(t, r), "slot-2"); r.code != output.ExitFailure ||
				c.Result != checkFail || c.Reason != reason+"; upool doctor --fix drops it" {
				t.Errorf("without --fix: exit %d, slot-2 %s %q; want 1 and a failure saying "+
					"--fix drops it", r.code, c.Result, c.Reason)
			}
			if names := p.slotNames(t); len(names) != 2 {
				t.Errorf("slots without --fix = %v, want both kept", names)
			}

			r = runIn(t, p.Env, p.Repo, "doctor", "--fix", "--json")
			if r.code != output.ExitOK {
				t.Errorf("--fix: exit = %d, want %d", r.code, output.ExitOK)
			}
			p.checkResults(t, doctorChecks(t, r), "slot-2", checkPass,
				"fixed: dropped it from the pool, since "+reason)
			if names := p.slotNames(t); strings.Join(names, " ") != "slot-1" {
				t.Errorf("slots after --fix = %v, want slot-1 alone", names)
			}
			if p.listed(p.slot("slot-2")) {
				t.Errorf("git worktree list still lists slot-2 after --fix")
			}

			r = runIn(t, p.Env, p.Repo, "doctor", "--json")
			if checks := doctorChecks(t, r); r.code != output.ExitOK ||
				len(checks) != len(wantChecks())-1 {
				t.Errorf("doctor after --fix: exit %d, checks %+v; want 0, without slot-2",
					r.code, checks)
			}
		})
	}
}

// --fix never touches a leased, unconfirmed, parked or needs-reset slot, nor one whose directory
// is still on disk, which may hold work: each stays reported, and stays in the pool.
func TestDoctorFixLeavesEveryOtherMissingSlot(t *testing.T) {
	gone := func(t *testing.T, p harnessPool) {
		if err := os.RemoveAll(p.slot("slot-2")); err != nil {
			t.Fatal(err)
		}
	}
	lease := &pool.Lease{LeaseID: "id", Holder: "alice", AcquiredAt: clock, Confirmed: true,
		Lane: "script"}
	tests := []struct {
		name   string
		state  pool.State
		lease  *pool.Lease
		breaks func(t *testing.T, p harnessPool)
	}{
		{"leased", pool.StateLeased, lease, gone},
		{"leased, unconfirmed", pool.StateLeasedUnconfirmed, lease, gone},
		{"parked", pool.StateParked, nil, gone},
		{"needs-reset", pool.StateNeedsReset, nil, gone},
		{"on disk but unregistered", pool.StateReady, nil, func(t *testing.T, p harnessPool) {
			if err := os.RemoveAll(p.Path(".git/worktrees/slot-2")); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newHarnessPool(t)
			p.update(t, func(pl *pool.Pool) {
				pl.Slots[1].State, pl.Slots[1].Lease = tt.state, tt.lease
			})
			tt.breaks(t, p)
			_, statErr := os.Stat(p.slot("slot-2"))

			r := runIn(t, p.Env, p.Repo, "doctor", "--fix", "--json")
			c := findCheck(t, doctorChecks(t, r), "slot-2")
			if r.code != output.ExitFailure || c.Result != checkFail ||
				strings.Contains(c.Reason, "--fix") {
				t.Errorf("exit %d, slot-2 %s %q; want 1 and a failure --fix does not offer to "+
					"repair", r.code, c.Result, c.Reason)
			}
			if names := p.slotNames(t); len(names) != 2 {
				t.Errorf("slots = %v, want slot-2 kept", names)
			}
			if _, err := os.Stat(p.slot("slot-2")); (err == nil) != (statErr == nil) {
				t.Errorf("slot-2 on disk: before %v, after %v; want it as it was", statErr, err)
			}
		})
	}
}
