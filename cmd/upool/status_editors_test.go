package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/weslo/upool/internal/ledger"
)

// The tests of the Editors status lists with their charges (#287), on newStatusHome's two pools
// and a ledger written as `upool open` and a seed refresh would leave it.

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

// equalEditors reports whether got and want hold the same Editors in the same order.
func equalEditors(got, want []editorStatus) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
