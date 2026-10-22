package main

import (
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/weslo/upool/internal/git"
	"github.com/weslo/upool/internal/output"
	"github.com/weslo/upool/internal/pool"
	"github.com/weslo/upool/internal/proc"
	"github.com/weslo/upool/internal/testenv"
)

// The tests of `upool take`, on the harness pool (harness_test.go): slot-1 and slot-2, ready and
// detached at base.

// loadPool returns the harness pool's state.json.
func (p harnessPool) loadPool(t *testing.T) pool.Pool {
	t.Helper()
	pl, err := pool.Load(p.Home, p.key)
	if err != nil {
		t.Fatal(err)
	}
	return pl
}

// worktree returns git's record of the worktree at path in the harness pool.
func (p harnessPool) worktree(t *testing.T, path string) git.Worktree {
	t.Helper()
	wts, err := git.Client{Runner: git.Exec{}, Env: p.Environ()}.WorktreeList(t.Context(), p.Repo)
	if err != nil {
		t.Fatal(err)
	}
	for _, wt := range wts {
		if wt.Path == path {
			return wt
		}
	}
	t.Fatalf("git worktree list does not list %s", path)
	return git.Worktree{}
}

// checkNothingLeased fails the test unless both slots are still ready with no lease, and neither
// worktree is locked.
func (p harnessPool) checkNothingLeased(t *testing.T) {
	t.Helper()
	for _, s := range p.loadPool(t).Slots {
		if s.State != pool.StateReady || s.Lease != nil {
			t.Errorf("%s: %s, lease %+v; want ready with none", s.Name, s.State, s.Lease)
		}
		if p.worktree(t, s.Path).Locked {
			t.Errorf("%s is locked, want it unlocked", s.Name)
		}
	}
}

// leaseIDPattern is a lease_id: 16 random bytes in hex.
var leaseIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

func TestTakePrintsOnlyThePath(t *testing.T) {
	p := newHarnessPool(t)
	r := runIn(t, p.Env, p.Repo, "take", "feature-x", "--holder", "alice")
	if r.code != output.ExitOK || r.stdout != p.slot("slot-1")+"\n" || r.stderr != "" {
		t.Fatalf("exit %d, stdout %q, stderr %q; want 0, the path of slot-1 alone, and nothing",
			r.code, r.stdout, r.stderr)
	}
	s := p.loadPool(t).Slots[0]
	l := s.Lease
	if s.State != pool.StateLeased || l == nil || !leaseIDPattern.MatchString(l.LeaseID) ||
		l.Holder != "alice" || l.WorktreePath != s.Path || l.HolderPID != nil ||
		l.HolderPIDStart != nil || !l.AcquiredAt.Equal(clock) || !l.Confirmed ||
		l.Lane != "script" || l.NameHint != "" {
		t.Errorf("slot-1 = %s, lease %+v; want leased by alice: a random lease_id, its path, no "+
			"holder PID, acquired at the clock, confirmed, lane script", s.State, l)
	}
	if s2 := p.loadPool(t).Slots[1]; s2.State != pool.StateReady || s2.Lease != nil {
		t.Errorf("slot-2 = %s, lease %+v; want it untouched", s2.State, s2.Lease)
	}

	r = runIn(t, p.Env, p.Repo, "take", "feature-y", "--json")
	env := decode(t, r.stdout)
	data, _ := json.Marshal(env.Data) // it was decoded from JSON, so it encodes
	var got takeReport
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	leased := p.loadPool(t).Slots[1]
	want := takeReport{Path: p.slot("slot-2"), Slot: "slot-2", Branch: "feature-y",
		LeaseID: leased.Lease.LeaseID, Repo: p.Repo}
	if r.code != output.ExitOK || !env.Success || got != want {
		t.Errorf("--json: exit %d, envelope %+v, data %+v; want 0 and %+v", r.code, env, got,
			want)
	}
}

// take leases a slot without a git worktree lock: the desktop app does not archive a locked
// worktree (A27).
func TestTakeLeavesTheWorktreeUnlocked(t *testing.T) {
	p := newHarnessPool(t)
	runIn(t, p.Env, p.Repo, "take", "feature-x")
	if s := p.loadPool(t).Slots[0]; s.State != pool.StateLeased || s.Lease == nil {
		t.Fatalf("slot-1 = %s, lease %+v; want leased", s.State, s.Lease)
	}
	porcelain := p.Git("worktree", "list", "--porcelain")
	if !strings.Contains(porcelain, "worktree "+p.slot("slot-1")+"\n") ||
		strings.Contains(porcelain, "\nlocked") {
		t.Errorf("git worktree list --porcelain =\n%s\nwant slot-1 listed and nothing locked",
			porcelain)
	}
}

// A new branch starts at --base, or at the pool's base without it; an existing branch is checked
// out where it is (A18).
func TestTakeCreatesBranchFromBase(t *testing.T) {
	p := newHarnessPool(t)
	p.Git("checkout", "-q", "-b", "release")
	p.WriteFile("RELEASE.md", "released\n")
	p.Git("add", "RELEASE.md")
	p.Git("commit", "-q", "-m", "release")
	release := p.Git("rev-parse", "HEAD")
	p.Git("checkout", "-q", testenv.Base)
	p.Git("branch", "existing", release)

	tests := []struct {
		args []string
		want string // the commit the branch is at
	}{
		{[]string{"take", "from-base"}, p.BaseCommit},
		{[]string{"take", "from-release", "--base", "release"}, release},
	}
	for i, tt := range tests {
		r := runIn(t, p.Env, p.Repo, tt.args...)
		slot := p.slot([]string{"slot-1", "slot-2"}[i])
		if r.code != output.ExitOK || r.stdout != slot+"\n" {
			t.Fatalf("%v: exit %d, stdout %q, stderr %q", tt.args, r.code, r.stdout, r.stderr)
		}
		if got := p.Git("-C", slot, "symbolic-ref", "--short", "HEAD"); got != tt.args[1] {
			t.Errorf("%v: HEAD is %s, want the branch %s", tt.args, got, tt.args[1])
		}
		if got := p.Git("rev-parse", tt.args[1]); got != tt.want {
			t.Errorf("%v: %s is at %s, want %s", tt.args, tt.args[1], got, tt.want)
		}
	}

	q := newHarnessPool(t)
	q.Git("branch", "existing")
	q.Git("checkout", "-q", "-b", "elsewhere")
	q.WriteFile("ELSEWHERE.md", "x\n")
	q.Git("add", "ELSEWHERE.md")
	q.Git("commit", "-q", "-m", "elsewhere")
	q.Git("checkout", "-q", testenv.Base)
	r := runIn(t, q.Env, q.Repo, "take", "existing", "--base", "elsewhere")
	if r.code != output.ExitOK {
		t.Fatalf("existing branch: exit %d, stderr %q", r.code, r.stderr)
	}
	if got := q.Git("-C", q.slot("slot-1"), "rev-parse", "HEAD"); got != q.BaseCommit {
		t.Errorf("existing branch: slot-1 HEAD = %s, want the branch where it was, %s", got,
			q.BaseCommit)
	}
}

// A branch git has checked out in a worktree, the main checkout or a slot, is refused, naming the
// worktree, and nothing is leased (A18).
func TestTakeRefusesBranchCheckedOutElsewhere(t *testing.T) {
	p := newHarnessPool(t)
	r := runIn(t, p.Env, p.Repo, "take", testenv.Base)
	want := "upool: branch main is checked out in " + p.Repo + "; a branch can be checked out " +
		"in one worktree at a time\n"
	if r.code != output.ExitFailure || r.stdout != "" || r.stderr != want {
		t.Errorf("exit %d, stdout %q, stderr %q; want 1, nothing, and %q", r.code, r.stdout,
			r.stderr, want)
	}
	p.checkNothingLeased(t)

	runIn(t, p.Env, p.Repo, "take", "feature-x")
	r = runIn(t, p.Env, p.Repo, "take", "feature-x")
	if r.code != output.ExitFailure || !strings.Contains(r.stderr,
		"branch feature-x is checked out in "+p.slot("slot-1")) {
		t.Errorf("exit %d, stderr %q; want 1, naming slot-1", r.code, r.stderr)
	}
	if s := p.loadPool(t).Slots[1]; s.State != pool.StateReady || s.Lease != nil {
		t.Errorf("slot-2 = %s, lease %+v; want it not leased", s.State, s.Lease)
	}
}

// liveOwner is an owner for a warming slot that is alive throughout the test: this process, with
// its real start time. Off macOS, where internal/proc reads no start times and so no owner can be
// shown dead, the start time is the test clock's.
func liveOwner(t *testing.T) (int, time.Time) {
	t.Helper()
	pid := os.Getpid()
	start, err := (proc.System{}).Start(pid)
	if errors.Is(err, errors.ErrUnsupported) {
		return pid, clock
	}
	if err != nil {
		t.Fatal(err)
	}
	return pid, start
}

// With no slot to hand out, take names each slot's skip reason on stderr and, given --no-create,
// exits 1; a ready slot that left the disk, git's list or its backlink is skipped as
// missing-from-worktree-list.
func TestTakeSkipsLeasedAndParked(t *testing.T) {
	lease := &pool.Lease{LeaseID: "id", Holder: "bob", AcquiredAt: clock, Confirmed: true,
		Lane: "script"}
	unconfirmed := &pool.Lease{LeaseID: "id2", AcquiredAt: clock, Lane: "script"}
	pid := os.Getpid()
	tests := []struct {
		name   string
		slot2  func(t *testing.T, p harnessPool, s *pool.Slot)
		reason string
	}{
		{"leased", func(_ *testing.T, _ harnessPool, s *pool.Slot) {
			s.State, s.Lease = pool.StateLeased, lease
		}, "leased"},
		{"ready, but with a lease", func(_ *testing.T, _ harnessPool, s *pool.Slot) {
			s.Lease = lease
		}, "leased"},
		{"parked", func(_ *testing.T, _ harnessPool, s *pool.Slot) {
			s.State = pool.StateParked
		}, "parked"},
		{"unconfirmed", func(_ *testing.T, _ harnessPool, s *pool.Slot) {
			s.State, s.Lease = pool.StateLeasedUnconfirmed, unconfirmed
		}, "unconfirmed"},
		{"creating", func(_ *testing.T, _ harnessPool, s *pool.Slot) {
			s.State, s.OwnerPID, s.OwnerPIDStart = pool.StateCreating, &pid, &clock
		}, "creating"},
		{"warming", func(t *testing.T, _ harnessPool, s *pool.Slot) {
			owner, start := liveOwner(t)
			s.State, s.OwnerPID, s.OwnerPIDStart = pool.StateWarming, &owner, &start
		}, "warming"},
		{"needs-reset", func(_ *testing.T, _ harnessPool, s *pool.Slot) {
			s.State = pool.StateNeedsReset
		}, "needs-reset"},
		{"its directory gone", func(t *testing.T, p harnessPool, _ *pool.Slot) {
			if err := os.RemoveAll(p.slot("slot-2")); err != nil {
				t.Fatal(err)
			}
		}, "missing-from-worktree-list"},
		{"removed from git's list", func(t *testing.T, p harnessPool, _ *pool.Slot) {
			if err := os.RemoveAll(p.Path(".git/worktrees/slot-2")); err != nil {
				t.Fatal(err)
			}
		}, "missing-from-worktree-list"},
		{"a backlink elsewhere", func(_ *testing.T, p harnessPool, _ *pool.Slot) {
			p.WriteFile(".claude/worktrees/slot-2/.git", "gitdir: "+p.Home+"\n")
		}, "missing-from-worktree-list"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newHarnessPool(t)
			p.update(t, func(pl *pool.Pool) {
				pl.Slots[0].State, pl.Slots[0].Lease = pool.StateLeased, lease
				tt.slot2(t, p, &pl.Slots[1])
			})
			before := p.loadPool(t)
			r := runIn(t, p.Env, p.Repo, "take", "feature-x", "--no-create")
			want := "upool: skipping slot-1: leased\nupool: skipping slot-2: " + tt.reason +
				"\nupool: no slot of " + p.Repo + " is ready\n"
			if r.code != output.ExitFailure || r.stdout != "" || r.stderr != want {
				t.Errorf("exit %d, stdout %q, stderr %q; want 1, nothing, and\n%s", r.code,
					r.stdout, r.stderr, want)
			}
			after, _ := json.Marshal(p.loadPool(t))
			if was, _ := json.Marshal(before); string(after) != string(was) {
				t.Errorf("state.json = %s, want it unchanged: %s", after, was)
			}
		})
	}
}

// --timeout S retries once a second: a slot freed meanwhile is taken at the next try, and with
// none freed take gives up once S seconds have passed, having named the skip reasons once. The
// waits pass on testing/synctest's clock, so the test does not sleep.
func TestTakeWaitsUntilTimeout(t *testing.T) {
	lease := &pool.Lease{LeaseID: "id", Holder: "bob", AcquiredAt: clock, Confirmed: true,
		Lane: "script"}
	leaseBoth := func(pl *pool.Pool) {
		for i := range pl.Slots {
			pl.Slots[i].State, pl.Slots[i].Lease = pool.StateLeased, lease
		}
	}

	t.Run("none freed", func(t *testing.T) {
		p := newHarnessPool(t)
		p.update(t, leaseBoth)
		synctest.Test(t, func(t *testing.T) {
			start := time.Now()
			r := runAt(t, p.Env, p.Repo, time.Now, "take", "feature-x", "--timeout", "3",
				"--no-create")
			if waited := time.Since(start); waited != 3*time.Second {
				t.Errorf("take waited %s, want 3s", waited)
			}
			want := "upool: skipping slot-1: leased\nupool: skipping slot-2: leased\n" +
				"upool: no slot of " + p.Repo + " became ready within 3s\n"
			if r.code != output.ExitFailure || r.stdout != "" || r.stderr != want {
				t.Errorf("exit %d, stdout %q, stderr %q; want 1, nothing, and\n%s", r.code,
					r.stdout, r.stderr, want)
			}
		})
	})

	t.Run("one freed", func(t *testing.T) {
		p := newHarnessPool(t)
		p.update(t, leaseBoth)
		synctest.Test(t, func(t *testing.T) {
			start := time.Now()
			go func() {
				time.Sleep(1500 * time.Millisecond)
				p.update(t, func(pl *pool.Pool) {
					pl.Slots[1].State, pl.Slots[1].Lease = pool.StateReady, nil
				})
			}()
			r := runAt(t, p.Env, p.Repo, time.Now, "take", "feature-x", "--timeout", "5",
				"--no-create")
			if waited := time.Since(start); waited != 2*time.Second {
				t.Errorf("take waited %s, want 2s: the first try after the slot was freed", waited)
			}
			if r.code != output.ExitOK || r.stdout != p.slot("slot-2")+"\n" {
				t.Errorf("exit %d, stdout %q, stderr %q; want 0 and slot-2's path", r.code,
					r.stdout, r.stderr)
			}
		})
	})
}

// A lease whose checkout fails is undone: the slot is ready again, its worktree unlocked.
func TestTakeUndoesTheLeaseWhenTheCheckoutFails(t *testing.T) {
	p := newHarnessPool(t)
	r := runIn(t, p.Env, p.Repo, "take", "feature-x", "--base", "no-such-ref")
	if r.code != output.ExitFailure || r.stdout != "" ||
		!strings.Contains(r.stderr, "no-such-ref") {
		t.Errorf("exit %d, stdout %q, stderr %q; want 1 and git's refusal of no-such-ref",
			r.code, r.stdout, r.stderr)
	}
	p.checkNothingLeased(t)
	if got := p.Git("-C", p.slot("slot-1"), "status", "--porcelain"); got != "" {
		t.Errorf("slot-1 status = %q, want clean", got)
	}
}

func TestTakeRefusesBadArguments(t *testing.T) {
	p := newHarnessPool(t)
	tests := []struct {
		args []string
		want string
	}{
		{[]string{"take"}, "take needs one branch name; usage: upool " + takeUsage},
		{[]string{"take", "a", "b"}, "take needs one branch name; usage: upool " + takeUsage},
		{[]string{"take", "bad..name"}, `"bad..name" is not a valid branch name`},
		{[]string{"take", "x", "--timeout", "-1"}, "--timeout -1 is negative"},
	}
	for _, tt := range tests {
		r := runIn(t, p.Env, p.Repo, tt.args...)
		if r.code != output.ExitFailure || !strings.Contains(r.stderr, tt.want) {
			t.Errorf("%v: exit %d, stderr %q; want 1 and %q", tt.args, r.code, r.stderr, tt.want)
		}
	}
	p.checkNothingLeased(t)

	outside := tempDir(t)
	r := runIn(t, p.Env, outside, "take", "x")
	if r.code != output.ExitFailure || !strings.Contains(r.stderr,
		outside+" is not inside a registered pool") {
		t.Errorf("outside a pool: exit %d, stderr %q; want 1", r.code, r.stderr)
	}
}
