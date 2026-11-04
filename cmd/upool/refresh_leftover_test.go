package main

import (
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/weslo/upool/internal/ledger"
	"github.com/weslo/upool/internal/output"
	"github.com/weslo/upool/internal/pool"
	"github.com/weslo/upool/internal/unity"
)

// The tests of the re-warm's close when a plain close leaves the ready Editor running, and of the
// Editor a failed close left live in a warming slot (A48; #273), on the harness pool of
// refresh_fetch_test.go.

// rewarmCallsBeforeClose are the fake's calls of a move and re-warm of slot-1 up to the close: the
// cache key twice, open, `status --until-ready` and save_all.
func rewarmCallsBeforeClose(slot string) [][]string {
	return [][]string{{"cache", "key", slot, "--format", "json"},
		{"cache", "key", slot, "--format", "json"},
		{"open", slot, "--args", "-batchmode -automated", "--format", "json"},
		{"status", "--until-ready", "--timeout", "300", "--format", "json"},
		{"command", "save_all", "--format", "json"}}
}

// A re-warm's plain close that leaves the ready Editor running, unreachable, is sent again, and
// when that one closes it the slot ends ready and re-warmed: two plain closes in the fake's log,
// no --force, the ledger empty (A48; #273). The wait between closes passes on testing/synctest's
// clock.
func TestRefreshRetriesTheRewarmClose(t *testing.T) {
	p, origin := fetchPool(t)
	upstream := pushUpstream(t, p, origin)
	replies := append(rewarmReplies(t, p, nil)[:5], closeUnreachableReply, closeHeadlessReply)
	u := p.installUnity(t, replies...)
	var r result
	synctest.Test(t, func(t *testing.T) {
		// The Editor runs for the check after the failed close, and is gone once the second
		// close has returned.
		r = runRefresh(t, p, newRewarmProcs(2, nil), "--fetch")
	})
	if r.code != output.ExitOK || !strings.Contains(r.stdout, "and re-warmed it in") {
		t.Errorf("exit %d, stdout %q, stderr %q; want 0 and slot-1 re-warmed", r.code, r.stdout,
			r.stderr)
	}
	slot := p.slot("slot-1")
	plain := []string{"close", slot, "--format", "json"}
	want := append(rewarmCallsBeforeClose(slot), plain, plain)
	if got := callArgs(t, p, u); !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("unity calls = %q, want %q: the plain close twice", got, want)
	}
	checkSlot1(t, p, upstream, false)
	checkEditors(t, p)
}

// A ready Editor that plain closes leave running past unity.CloseRetryTimeout is closed with
// --force, the instance files it was terminated with go once `unity status` lists none (A45), and
// the slot ends ready and re-warmed, since the Editor was ready and the Library is current (A48;
// #273).
func TestRefreshForcesTheRewarmCloseAfterTheBound(t *testing.T) {
	p, origin := fetchPool(t)
	upstream := pushUpstream(t, p, origin)
	replies := append(rewarmReplies(t, p, nil)[:5], closeUnreachableReply, closeForcedReply,
		noEditorsReply)
	u := p.installUnity(t, replies...)
	// Each reading of the clock is past the retry bound, so the first failed close is the last
	// plain one.
	var ticks atomic.Int64
	now := func() time.Time {
		return clock.Add(time.Duration(ticks.Add(1)) * unity.CloseRetryTimeout)
	}
	r := runProcs(t, p.Env, p.Repo, newRewarmProcs(2, nil), now, "refresh", "--fetch")
	if r.code != output.ExitOK || !strings.Contains(r.stdout, "and re-warmed it in") {
		t.Errorf("exit %d, stdout %q, stderr %q; want 0 and slot-1 re-warmed", r.code, r.stdout,
			r.stderr)
	}
	slot := p.slot("slot-1")
	want := append(rewarmCallsBeforeClose(slot), []string{"close", slot, "--format", "json"},
		[]string{"close", slot, "--force", "--format", "json"},
		[]string{"status", "--format", "json"})
	if got := callArgs(t, p, u); !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("unity calls = %q, want %q: a plain close, then --force", got, want)
	}
	checkSlot1(t, p, upstream, false)
	checkEditors(t, p)
}

// A refresh closes the Editor an earlier re-warm left live in a warming slot whose owner died:
// a plain close, then the slot checked again and returned to ready, the ledger without the
// Editor, and refresh's timings naming a close step for the slot (A40; (f) transition 22; #273).
// A live Editor upool did not open is left alone, with a warning, and the slot stays warming.
func TestRefreshClosesALeftoverRewarmEditor(t *testing.T) {
	warming := func(t *testing.T, p harnessPool) {
		t.Helper()
		owner, start := 4100, time.Unix(1, 0)
		p.update(t, func(pl *pool.Pool) {
			s := &pl.Slots[0]
			s.State, s.OwnerPID, s.OwnerPIDStart = pool.StateWarming, &owner, &start
		})
	}

	t.Run("closed and returned to ready", func(t *testing.T) {
		p, _ := fetchPool(t)
		warming(t, p)
		slot := p.slot("slot-1")
		writeLedger(t, p, ledger.Editor{Project: slot, PID: capturedPID, Mode: modeHeadless,
			OpenedByUpool: true, ChargeGB: 2.5})
		u := p.installUnity(t, closeHeadlessReply)
		// The Editor runs for the check that finds it live, and is gone once `unity close` has
		// returned.
		r := runRefresh(t, p, newRewarmProcs(1, nil), "--json")
		if r.code != output.ExitOK {
			t.Fatalf("exit %d, stdout %q, stderr %q; want 0", r.code, r.stdout, r.stderr)
		}
		env := decode(t, r.stdout)
		data, _ := env.Data.(map[string]any)
		recovered, _ := data["recovered"].([]any)
		if len(recovered) != 1 {
			t.Fatalf("recovered = %v, want slot-1", recovered)
		}
		got, _ := recovered[0].(map[string]any)
		if got["slot"] != "slot-1" || got["pid"] != float64(capturedPID) || got["state"] != "ready" {
			t.Errorf("recovered[0] = %v, want slot-1, process %d, ready", got, capturedPID)
		}
		progress := "upool: closing the Editor a re-warm left in slot-1, process 25147\n"
		if !strings.Contains(r.stderr, progress) {
			t.Errorf("stderr = %q, want it to say %q", r.stderr, progress)
		}
		want := [][]string{{"close", slot, "--format", "json"}}
		if calls := callArgs(t, p, u); !slices.EqualFunc(calls, want, slices.Equal) {
			t.Errorf("unity calls = %q, want the plain close alone", calls)
		}
		if env.Timings == nil || !slices.ContainsFunc(env.Timings.Steps, func(s output.Step) bool {
			return s.Name == "close" && s.Slot == "slot-1"
		}) {
			t.Errorf("timings = %+v, want a close step naming slot-1", env.Timings)
		}
		checkSlot1(t, p, p.BaseCommit, false)
		checkEditors(t, p)
	})

	t.Run("an Editor upool did not open", func(t *testing.T) {
		p, _ := fetchPool(t)
		warming(t, p)
		slot := p.slot("slot-1")
		writeLedger(t, p, ledger.Editor{Project: slot, PID: capturedPID, Mode: "gui",
			ChargeGB: 2.5})
		u := p.installUnity(t)
		r := runRefresh(t, p, procsAlive(os.Getpid(), capturedPID))
		warning := "upool: warning: left slot-1 warming: an Editor upool did not open, process " +
			"25147, has it open\n"
		if r.code != output.ExitOK || !strings.Contains(r.stderr, warning) {
			t.Errorf("exit %d, stderr %q; want 0 and %q", r.code, r.stderr, warning)
		}
		if calls := u.Calls(); len(calls) != 0 {
			t.Errorf("unity calls = %+v, want none", calls)
		}
		if s := p.loadPool(t).Slots[0]; s.State != pool.StateWarming {
			t.Errorf("slot-1 = %s, want warming", s.State)
		}
	})
}
