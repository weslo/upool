package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/weslo/upool/internal/output"
	"github.com/weslo/upool/internal/pool"
)

// The tests of `upool path`, on the harness pool (harness_test.go), with branches checked out by
// `upool take` as a person would.

// stateFile returns the bytes of the harness pool's state.json.
func (p harnessPool) stateFile(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(p.Home, ".upool", "pools", p.key, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// From anywhere in the pool, path prints the slot that has the branch checked out, and --json
// gives which's data for it.
func TestPathFindsTheSlotOnTheBranch(t *testing.T) {
	p := newHarnessPool(t)
	runIn(t, p.Env, p.Repo, "take", "feature-x")
	runIn(t, p.Env, p.Repo, "take", "feature-y")
	before := p.stateFile(t)
	for _, dir := range []string{p.Repo, p.Path("Assets/Scripts"), p.slot("slot-1")} {
		r := runIn(t, p.Env, dir, "path", "feature-y")
		if r.code != output.ExitOK || r.stdout != p.slot("slot-2")+"\n" || r.stderr != "" {
			t.Errorf("from %s: exit %d, stdout %q, stderr %q; want 0 and slot-2's path alone",
				dir, r.code, r.stdout, r.stderr)
		}
	}

	r := runIn(t, p.Env, p.Repo, "path", "feature-x", "--json")
	env := decode(t, r.stdout)
	data, _ := json.Marshal(env.Data) // it was decoded from JSON, so it encodes
	var got, want whichReport
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	want = *newWhichReport(p.loadPool(t).Slots[0], p.Repo)
	if r.code != output.ExitOK || !env.Success || got.Name != want.Name ||
		got.Path != want.Path || got.State != want.State || got.Repo != want.Repo ||
		got.Lease == nil || *got.Lease != *want.Lease {
		t.Errorf("--json: exit %d, data %s; want 0 and %+v", r.code, data, want)
	}
	if after := p.stateFile(t); string(after) != string(before) {
		t.Errorf("state.json changed: %s, want %s", after, before)
	}
}

// With no slot on the branch path exits 1 with NO_SUCH_SLOT, also when a worktree that is not a
// slot has it: the main checkout, or a worktree a person added.
func TestPathWithNoSlotOnTheBranchFails(t *testing.T) {
	p := newHarnessPool(t)
	other := filepath.Join(filepath.Dir(p.Repo), "other")
	p.Git("worktree", "add", "-q", "-b", "elsewhere", other)
	tests := []struct{ name, want string }{
		{"no-such-branch", "no slot of " + p.Repo + " has branch no-such-branch checked out"},
		{"main", "no slot of " + p.Repo + " has branch main checked out; " + p.Repo +
			" has it, and is not a slot"},
		{"elsewhere", "no slot of " + p.Repo + " has branch elsewhere checked out; " + other +
			" has it, and is not a slot"},
	}
	for _, tt := range tests {
		r := runIn(t, p.Env, p.Repo, "path", tt.name, "--json")
		env := decode(t, r.stdout)
		if r.code != output.ExitFailure || env.Success || len(env.Errors) != 1 ||
			env.Errors[0].Code != string(output.CodeNoSuchSlot) || env.Errors[0].Message != tt.want {
			t.Errorf("%s: exit %d, errors %+v; want 1 and NO_SUCH_SLOT, %q", tt.name, r.code,
				env.Errors, tt.want)
		}
		r = runIn(t, p.Env, p.Repo, "path", tt.name)
		if r.code != output.ExitFailure || r.stdout != "" || r.stderr != "upool: "+tt.want+"\n" {
			t.Errorf("%s: exit %d, stdout %q, stderr %q; want 1 and the message", tt.name, r.code,
				r.stdout, r.stderr)
		}
	}
	for _, s := range p.loadPool(t).Slots {
		if s.State != pool.StateReady {
			t.Errorf("%s: %s, want it left ready", s.Name, s.State)
		}
	}
}

func TestPathRefusesBadArguments(t *testing.T) {
	p := newHarnessPool(t)
	for _, args := range [][]string{{"path"}, {"path", "a", "b"}} {
		r := runIn(t, p.Env, p.Repo, args...)
		if want := "path needs one branch name; usage: upool " + pathUsage; r.code !=
			output.ExitFailure || !strings.Contains(r.stderr, want) {
			t.Errorf("%v: exit %d, stderr %q; want 1 and %q", args, r.code, r.stderr, want)
		}
	}
	outside := tempDir(t)
	r := runIn(t, p.Env, outside, "path", "x")
	if r.code != output.ExitFailure || !strings.Contains(r.stderr,
		outside+" is not inside a registered pool") {
		t.Errorf("outside a pool: exit %d, stderr %q; want 1", r.code, r.stderr)
	}
}
