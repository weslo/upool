package main

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/weslo/upool/internal/ledger"
)

// The tests of the Editors status lists with their charges (#287) and how long each has been open
// (#281), on newStatusHome's two pools and a ledger written as `upool open` and a seed refresh
// would leave it.

// statusEditors writes a ledger under h with an Editor in each of down's slot-1 (gui), slot-3
// (headless, cold-importing) and main checkout (a seed refresh's), one in castle's slot-2, and one
// in a directory no pool has, and returns them in that order.
func statusEditors(t *testing.T, h statusHome) []ledger.Editor {
	t.Helper()
	worktree := func(repo, name string) string {
		return filepath.Join(repo, ".claude", "worktrees", name)
	}
	editors := []ledger.Editor{
		{Project: worktree(h.down, "slot-1"), PID: 51207, Mode: "gui", OpenedByUpool: true,
			ChargeGB: 6},
		{Project: worktree(h.down, "slot-3"), PID: 51388, Mode: "headless", OpenedByUpool: true,
			ColdImport: true, ChargeGB: 4.5},
		{Project: h.down, PID: 51400, Mode: "headless", OpenedByUpool: true, ChargeGB: 2.5},
		{Project: worktree(h.castle, "slot-2"), PID: 51511, Mode: "gui", OpenedByUpool: true,
			ChargeGB: 6},
		{Project: filepath.Join(h.home, "elsewhere"), PID: 51622, Mode: "gui", ChargeGB: 6},
	}
	if err := ledger.Update(t.Context(), h.home, func(l *ledger.Ledger) error {
		l.Editors = editors
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return editors
}

// status lists each Editor the ledger charges in a pool after the pool's table, by slot, or by
// path for the main checkout's, with its mode, process and charge, and a cold import's marked;
// an Editor in no pool is not listed. With --all each pool lists its own.
func TestStatusShowsEachEditorsCharge(t *testing.T) {
	h := newStatusHome(t)
	statusEditors(t, h)
	down := []string{
		"slot-1's Editor: gui, process 51207, charged 6.0 GB",
		"slot-3's Editor: headless, process 51388, charged 4.5 GB as a cold import",
		h.down + "'s Editor: headless, process 51400, charged 2.5 GB",
	}
	castle := "slot-2's Editor: gui, process 51511, charged 6.0 GB"

	r := runStatus(t, h.home, h.down)
	checkSucceeded(t, r)
	lines := strings.Split(strings.TrimSuffix(r.stdout, "\n"), "\n")
	if got := lines[len(lines)-3:]; strings.Join(got, "\n") != strings.Join(down, "\n") {
		t.Errorf("stdout ends\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(down, "\n"))
	}
	if strings.Contains(r.stdout, "51511") || strings.Contains(r.stdout, "51622") {
		t.Errorf("stdout = %q, want no other pool's Editor and none outside a pool", r.stdout)
	}

	r = runStatus(t, h.home, h.down, "--all")
	checkSucceeded(t, r)
	pools := strings.Split(strings.TrimSuffix(r.stdout, "\n"), "\n\n")
	if len(pools) != 2 || !strings.HasSuffix(pools[0], strings.Join(down, "\n")) ||
		!strings.HasSuffix(pools[1], castle) || strings.Contains(r.stdout, "51622") {
		t.Errorf("status --all =\n%s\nwant down's three Editors, then castle's one", r.stdout)
	}
}

// --json lists a pool's Editors under editors, with the slot each has open, none for the main
// checkout's, and leaves the key out for a pool the ledger charges nothing in.
func TestStatusJSONListsEachEditorsCharge(t *testing.T) {
	h := newStatusHome(t)
	editors := statusEditors(t, h)
	r := runStatus(t, h.home, h.down, "--json", "--all")
	checkSucceeded(t, r)
	var env struct {
		Data struct {
			Pools []struct {
				Editors []editorStatus `json:"editors"`
			} `json:"pools"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &env); err != nil {
		t.Fatal(err)
	}
	want := []editorStatus{
		{Slot: "slot-1", Project: editors[0].Project, PID: 51207, Mode: "gui", ChargeGB: 6},
		{Slot: "slot-3", Project: editors[1].Project, PID: 51388, Mode: "headless",
			ColdImport: true, ChargeGB: 4.5},
		{Project: h.down, PID: 51400, Mode: "headless", ChargeGB: 2.5},
	}
	if p := env.Data.Pools; len(p) != 2 || !equalEditors(p[0].Editors, want) ||
		len(p[1].Editors) != 1 || p[1].Editors[0].Slot != "slot-2" {
		t.Errorf("pools' editors = %+v, want down's %+v and castle's slot-2", p, want)
	}

	// A pool with no Editor has no editors key, so the output of every other status test is
	// unchanged.
	h = newStatusHome(t)
	r = runStatus(t, h.home, h.down, "--json")
	checkSucceeded(t, r)
	if strings.Contains(r.stdout, `"editors"`) {
		t.Errorf("stdout = %s, want no editors key with an empty ledger", r.stdout)
	}
}

// equalEditors reports whether got and want hold the same Editors in the same order, their ready
// times compared as instants.
func equalEditors(got, want []editorStatus) bool {
	return slices.EqualFunc(got, want, func(a, b editorStatus) bool {
		ra, rb := a.ReadyAt, b.ReadyAt
		a.ReadyAt, b.ReadyAt = nil, nil
		if a != b || (ra == nil) != (rb == nil) {
			return false
		}
		return ra == nil || ra.Equal(*rb)
	})
}

// openEditors writes a ledger under h with Editors in down's slots that `upool open` saw ready
// 45m, exactly 2h, 2h0m59s, 2h1m and 2h50m before the test clock, in slot-1, slot-2, slot-4,
// slot-5 and slot-10, and one in slot-3 with no ready time, still opening as a cold import, and
// returns them in that order.
func openEditors(t *testing.T, h statusHome) []ledger.Editor {
	t.Helper()
	worktree := func(name string) string {
		return filepath.Join(h.down, ".claude", "worktrees", name)
	}
	ready := func(ago time.Duration) *time.Time {
		at := clock.Add(-ago)
		return &at
	}
	editors := []ledger.Editor{
		{Project: worktree("slot-1"), PID: 51207, Mode: "gui", OpenedByUpool: true, ChargeGB: 6,
			ReadyAt: ready(45 * time.Minute)},
		{Project: worktree("slot-2"), PID: 51208, Mode: "headless", OpenedByUpool: true,
			ChargeGB: 2.5, ReadyAt: ready(2 * time.Hour)},
		{Project: worktree("slot-4"), PID: 51209, Mode: "headless", OpenedByUpool: true,
			ChargeGB: 2.5, ReadyAt: ready(2*time.Hour + 59*time.Second)},
		{Project: worktree("slot-5"), PID: 51210, Mode: "gui", OpenedByUpool: true, ChargeGB: 6,
			ReadyAt: ready(2*time.Hour + time.Minute)},
		{Project: worktree("slot-10"), PID: 51211, Mode: "gui", OpenedByUpool: true, ChargeGB: 6,
			ReadyAt: ready(2*time.Hour + 50*time.Minute)},
		{Project: worktree("slot-3"), PID: 51388, Mode: "headless", OpenedByUpool: true,
			ColdImport: true, ChargeGB: 4.5},
	}
	if err := ledger.Update(t.Context(), h.home, func(l *ledger.Ledger) error {
		l.Editors = editors
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return editors
}

// The acceptance test of #281: status says how long each Editor has been open, since `upool open`
// saw it ready, and flags one open longer than two hours, telling its session to close it once its
// work is done. At 2h, and up to 2h0m59s, which it shows as 2h, it is not flagged yet; from 2h1m it
// is. An Editor with no ready time says nothing of how long it has been open.
func TestStatusShowsHowLongEachEditorHasBeenOpen(t *testing.T) {
	h := newStatusHome(t)
	openEditors(t, h)
	flag := ", over 2h: close it with upool close once its session's work is done"
	want := []string{
		"slot-1's Editor: gui, process 51207, charged 6.0 GB, open for 45m",
		"slot-2's Editor: headless, process 51208, charged 2.5 GB, open for 2h",
		"slot-4's Editor: headless, process 51209, charged 2.5 GB, open for 2h",
		"slot-5's Editor: gui, process 51210, charged 6.0 GB, open for 2h1m" + flag,
		"slot-10's Editor: gui, process 51211, charged 6.0 GB, open for 2h50m" + flag,
		"slot-3's Editor: headless, process 51388, charged 4.5 GB as a cold import",
	}
	r := runStatus(t, h.home, h.down)
	checkSucceeded(t, r)
	lines := strings.Split(strings.TrimSuffix(r.stdout, "\n"), "\n")
	if got := lines[len(lines)-len(want):]; !slices.Equal(got, want) {
		t.Errorf("stdout ends\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if strings.Contains(r.stdout, "idle") {
		t.Errorf("stdout = %q, want no word of idle: upool hears no Unity call of a session's",
			r.stdout)
	}
}

// --json gives each Editor its ready_at, in UTC, and open_long on one open longer than two hours,
// and leaves both out for an Editor the ledger has no ready time for, as it leaves open_long out
// for one not open that long.
func TestStatusJSONSaysWhenEachEditorWasReady(t *testing.T) {
	h := newStatusHome(t)
	editors := openEditors(t, h)
	r := runStatus(t, h.home, h.down, "--json")
	checkSucceeded(t, r)
	var env struct {
		Data struct {
			Pools []struct {
				Editors []editorStatus `json:"editors"`
			} `json:"pools"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &env); err != nil {
		t.Fatal(err)
	}
	want := make([]editorStatus, len(editors))
	for i, e := range editors {
		want[i] = editorStatus{Slot: filepath.Base(e.Project), Project: e.Project, PID: e.PID,
			Mode: e.Mode, ColdImport: e.ColdImport, ChargeGB: e.ChargeGB, ReadyAt: e.ReadyAt,
			OpenLong: i == 3 || i == 4}
	}
	if p := env.Data.Pools; len(p) != 1 || !equalEditors(p[0].Editors, want) {
		t.Errorf("pools' editors = %+v, want %+v", p, want)
	}
	if !strings.Contains(r.stdout, `"ready_at": "2026-10-19T11:15:00Z"`) {
		t.Errorf("stdout = %s, want slot-1's ready_at in UTC, 2026-10-19T11:15:00Z", r.stdout)
	}
	if n := strings.Count(r.stdout, `"ready_at"`); n != 5 {
		t.Errorf("stdout has %d ready_at keys, want 5: none for the Editor still opening", n)
	}
	if n := strings.Count(r.stdout, `"open_long": true`); n != 2 || strings.Contains(r.stdout,
		`"open_long": false`) {
		t.Errorf("stdout has %d open_long keys set, want 2 and none false", n)
	}
}
