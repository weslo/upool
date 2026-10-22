package hooks

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/weslo/upool/internal/pool"
)

// The tests of the lane guard, the PreToolUse hook on Edit, Write and NotebookEdit (docs/design.md,
// "Lanes: scripts in parallel, scenes one at a time, `.meta` never regenerated"), on a harness pool
// with slot-1 leased in the script lane.

// preToolUseCaptures are the captured PreToolUse requests, one per tool the guard is registered
// for (#86).
var preToolUseCaptures = []string{"pretooluse-edit.json", "pretooluse-write.json",
	"pretooluse-notebookedit.json"}

// pointAt returns req with the file its tool writes, file_path or notebook_path as the tool has
// it, set to path.
func pointAt(req Request, path string) Request {
	if req.ToolInput.NotebookPath != "" {
		req.ToolInput.NotebookPath = path
	} else {
		req.ToolInput.FilePath = path
	}
	return req
}

// setLane sets the lane of the lease on h's slot name.
func (h harness) setLane(t *testing.T, name, lane string) {
	t.Helper()
	err := pool.Update(context.Background(), h.e.Home, h.key, func(p *pool.Pool) error {
		for i := range p.Slots {
			if p.Slots[i].Name == name {
				p.Slots[i].Lease.Lane = lane
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// The acceptance test of #95: each captured request, pointed at a file in slot-1, which is leased
// in the script lane, is denied for a .unity file, with a reason naming `upool take --lane scene`,
// and allowed for a .cs file; with the lease in the scene lane both are allowed; and a .unity file
// outside every slot, in the main checkout, is allowed.
func TestLaneHook(t *testing.T) {
	h := newHarness(t, true)
	slot := h.slotPath("slot-1")
	h.leaseSlot(t, "slot-1", slot)
	scene := filepath.Join(slot, "Assets", "Scenes", "Main.unity")
	script := filepath.Join(slot, "Assets", "Scripts", "Player.cs")
	outside := filepath.Join(h.e.Repo, "Assets", "Scenes", "Main.unity")
	for _, lane := range []string{pool.LaneScript, pool.LaneScene} {
		h.setLane(t, "slot-1", lane)
		for _, capture := range preToolUseCaptures {
			req := h.capture(t, capture)
			for _, tt := range []struct {
				path string
				deny bool
			}{
				{scene, lane == pool.LaneScript},
				{script, false},
				{outside, false},
			} {
				d, err := LaneGuard(h.env, pointAt(req, tt.path))
				if err != nil || d.Deny != tt.deny {
					t.Errorf("%s, %s lane, %s: %+v, %v; want deny %t", capture, lane, tt.path, d,
						err, tt.deny)
				}
				if d.Deny && (!strings.Contains(d.Reason, "upool take <branch> --lane scene") ||
					!strings.Contains(d.Reason, filepath.Join("Assets", "Scenes", "Main.unity"))) {
					t.Errorf("%s: reason %q, want it to name the file and upool take --lane "+
						"scene", capture, d.Reason)
				}
			}
		}
	}
}

// Every kind of file only the scene lane writes is denied in a script lane, whatever the case of
// its extension; a relative path is resolved against the request's cwd; a slot with no lane
// recorded, from before lanes, is in the script lane.
func TestLaneHookGuardsEachKindOfFile(t *testing.T) {
	h := newHarness(t, true)
	slot := h.slotPath("slot-1")
	h.leaseSlot(t, "slot-1", slot)
	h.setLane(t, "slot-1", "")
	req := h.capture(t, "pretooluse-edit.json")
	for _, file := range []string{"Assets/Main.unity", "Assets/Hero.prefab", "Assets/Data.asset",
		"Assets/Player.cs.meta", "Assets/LOUD.UNITY"} {
		if d, err := LaneGuard(h.env, pointAt(req, filepath.Join(slot, file))); err != nil ||
			!d.Deny {
			t.Errorf("%s: %+v, %v; want it denied", file, d, err)
		}
	}
	req.Cwd = slot
	if d, err := LaneGuard(h.env, pointAt(req, "Assets/Main.unity")); err != nil || !d.Deny {
		t.Errorf("a relative path: %+v, %v; want it denied", d, err)
	}
}

// A denial is printed as Claude Code's PreToolUse decision; an allowed call prints nothing.
func TestLaneHookDecisionOutput(t *testing.T) {
	out, err := Decision{Deny: true, Reason: "no"}.Output()
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		HookSpecificOutput map[string]string `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("output %s: %v", out, err)
	}
	want := map[string]string{"hookEventName": "PreToolUse", "permissionDecision": "deny",
		"permissionDecisionReason": "no"}
	for k, v := range want {
		if got.HookSpecificOutput[k] != v {
			t.Errorf("output %s: %s = %q, want %q", out, k, got.HookSpecificOutput[k], v)
		}
	}
	if out, err := (Decision{}).Output(); err != nil || out != nil {
		t.Errorf("an allowed call's output = %q, %v; want nothing", out, err)
	}
}
