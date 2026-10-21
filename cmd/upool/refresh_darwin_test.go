package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/weslo/upool/internal/output"
	"github.com/weslo/upool/internal/pool"
	"github.com/weslo/upool/internal/proc"
	"github.com/weslo/upool/internal/testenv"
)

// The tests of `upool refresh`, the top-up the session-start hook spawns after a handout (#79),
// after a real `upool init`: macOS only, as the clone recipe is. ready is the default, 1, and
// max_slots the default, 3.

// setSlot changes slot-1 of e's pool with f.
func setSlot(t *testing.T, e *testenv.Env, f func(s *pool.Slot)) {
	t.Helper()
	err := pool.Update(t.Context(), e.Home, repoKey(t, e), func(p *pool.Pool) error {
		f(&p.Slots[0])
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// refresh runs upool refresh in e's main checkout and fails the test unless it exits 0.
func refresh(t *testing.T, e *testenv.Env) result {
	t.Helper()
	r := runIn(t, e, e.Repo, "refresh")
	if r.code != output.ExitOK {
		t.Fatalf("refresh: exit = %d, stdout %q, stderr %q; want %d", r.code, r.stdout, r.stderr,
			output.ExitOK)
	}
	return r
}

// liveJob is a refresh_job naming a process that is alive, this test process's parent, with its
// real start time.
func liveJob(t *testing.T) *pool.RefreshJob {
	t.Helper()
	start, err := (proc.System{}).Start(os.Getppid())
	if err != nil {
		t.Fatal(err)
	}
	return &pool.RefreshJob{PID: os.Getppid(), PIDStart: start}
}

// The acceptance test of #79: a needs-reset slot is reset to ready, and with it ready the pool has
// its one ready slot, so nothing is cloned; a pool below ready gets a clone; a second refresh
// while one is running exits 0 without working; and refresh_job is cleared when a refresh ends.
func TestRefreshTopUp(t *testing.T) {
	t.Run("needs-reset slot reset", func(t *testing.T) {
		e := newInitEnv(t)
		mustInit(t, e)
		setSlot(t, e, func(s *pool.Slot) { s.State = pool.StateNeedsReset })
		r := refresh(t, e)
		if want := "reset slot-1\nthe pool has 1 ready, as ready = 1 asks\n"; r.stdout != want {
			t.Errorf("stdout = %q, want %q", r.stdout, want)
		}
		if got := strings.Join(slotNamesOf(t, e), " "); got != "slot-1=ready" {
			t.Errorf("slots = %s, want slot-1 ready and no clone", got)
		}
	})
	t.Run("pool below ready", func(t *testing.T) {
		e := leasedPool(t)
		r := refresh(t, e)
		if want := "cloned slot-2\nthe pool has 1 ready, as ready = 1 asks\n"; r.stdout != want {
			t.Errorf("stdout = %q, want %q; stderr %q", r.stdout, want, r.stderr)
		}
		if got := strings.Join(slotNamesOf(t, e), " "); got != "slot-1=leased slot-2=ready" {
			t.Errorf("slots = %s, want slot-2 cloned ready", got)
		}
		p, err := pool.Load(e.Home, repoKey(t, e))
		if err != nil {
			t.Fatal(err)
		}
		if p.RefreshJob != nil {
			t.Errorf("refresh_job = %+v, want it cleared when the refresh ended", p.RefreshJob)
		}
	})
	t.Run("one already running", func(t *testing.T) {
		e := leasedPool(t)
		job := liveJob(t)
		err := pool.Update(t.Context(), e.Home, repoKey(t, e), func(p *pool.Pool) error {
			p.RefreshJob = job
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		r := refresh(t, e)
		want := "a refresh of " + e.Repo + " is already running as process " +
			strconv.Itoa(job.PID) + "\n"
		if r.stdout != want {
			t.Errorf("stdout = %q, want %q", r.stdout, want)
		}
		if got := strings.Join(slotNamesOf(t, e), " "); got != "slot-1=leased" {
			t.Errorf("slots = %s, want nothing cloned", got)
		}
		p, err := pool.Load(e.Home, repoKey(t, e))
		if err != nil {
			t.Fatal(err)
		}
		if p.RefreshJob == nil || p.RefreshJob.PID != job.PID {
			t.Errorf("refresh_job = %+v, want the running one's kept", p.RefreshJob)
		}
	})
}

// With the pool at ready already, refresh has nothing to do, and says only that. A creating or
// warming slot counts as ready, since it is ready once its clone or re-warm is done (A4).
func TestRefreshCountsCreatingAndWarmingAsReady(t *testing.T) {
	for _, state := range []pool.State{pool.StateReady, pool.StateCreating, pool.StateWarming} {
		t.Run(string(state), func(t *testing.T) {
			e := newInitEnv(t)
			mustInit(t, e)
			job := liveJob(t)
			setSlot(t, e, func(s *pool.Slot) {
				s.State = state
				if state != pool.StateReady {
					s.OwnerPID, s.OwnerPIDStart = &job.PID, &job.PIDStart
				}
			})
			r := refresh(t, e)
			if want := "the pool has 1 ready, as ready = 1 asks\n"; r.stdout != want ||
				r.stderr != "" {
				t.Errorf("stdout %q, stderr %q; want %q and nothing", r.stdout, r.stderr, want)
			}
			if got := strings.Join(slotNamesOf(t, e), " "); got != "slot-1="+string(state) {
				t.Errorf("slots = %s, want slot-1 alone, %s", got, state)
			}
		})
	}
}

// A refresh_job whose process is gone is no reason to stand aside: refresh takes its place.
func TestRefreshReplacesADeadJob(t *testing.T) {
	e := leasedPool(t)
	err := pool.Update(t.Context(), e.Home, repoKey(t, e), func(p *pool.Pool) error {
		p.RefreshJob = &pool.RefreshJob{PID: os.Getppid(), PIDStart: time.Unix(1, 0)}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if r := refresh(t, e); !strings.HasPrefix(r.stdout, "cloned slot-2\n") {
		t.Errorf("stdout = %q, want slot-2 cloned", r.stdout)
	}
}

// A needs-reset slot that is dirty, though it was clean when it was marked, is neither reset nor
// used as a seed, its file kept; the top-up says so and clones from the main checkout instead.
func TestRefreshLeavesADirtyNeedsResetSlot(t *testing.T) {
	e := newInitEnv(t)
	mustInit(t, e)
	setSlot(t, e, func(s *pool.Slot) { s.State = pool.StateNeedsReset })
	kept := filepath.Join(slotPath(e), "left-behind.txt")
	if err := os.WriteFile(kept, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := refresh(t, e)
	if r.stdout != "cloned slot-2\nthe pool has 1 ready, as ready = 1 asks\n" {
		t.Errorf("stdout = %q, want slot-2 cloned", r.stdout)
	}
	want := "did not reset slot-1: it is dirty, so it was not reset: ?? left-behind.txt"
	if !strings.Contains(r.stderr, want) {
		t.Errorf("stderr = %q, want it to contain %q", r.stderr, want)
	}
	if got := strings.Join(slotNamesOf(t, e), " "); got != "slot-1=needs-reset slot-2=ready" {
		t.Errorf("slots = %s, want slot-1 left needs-reset and slot-2 cloned", got)
	}
	if _, err := os.Stat(kept); err != nil {
		t.Errorf("%s is gone: %v", kept, err)
	}
}

// status announces the refresh job while its process is alive, in both forms, and says nothing of
// one whose process is gone.
func TestStatusShowsALiveRefreshJob(t *testing.T) {
	e := newInitEnv(t)
	mustInit(t, e)
	job := liveJob(t)
	for _, live := range []bool{true, false} {
		j := *job
		if !live {
			j.PIDStart = time.Unix(1, 0)
		}
		err := pool.Update(t.Context(), e.Home, repoKey(t, e), func(p *pool.Pool) error {
			p.RefreshJob = &j
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		r := runIn(t, e, e.Repo, "status")
		line := "refreshing: upool refresh is running as process " + strconv.Itoa(job.PID)
		if strings.Contains(r.stdout, line) != live {
			t.Errorf("live %t: stdout = %q, want the line %q only while it runs", live, r.stdout,
				line)
		}
		r = runIn(t, e, e.Repo, "status", "--json")
		if strings.Contains(r.stdout, `"pid": `+strconv.Itoa(job.PID)) != live {
			t.Errorf("live %t: --json = %s, want refresh_job only while it runs", live, r.stdout)
		}
	}
}
