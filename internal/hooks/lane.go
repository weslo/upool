package hooks

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/weslo/upool/internal/paths"
	"github.com/weslo/upool/internal/pool"
	"github.com/weslo/upool/internal/registry"
)

// eventPreToolUse is PreToolUse's hook_event_name, which the lane guard's decision names
// (testdata/captures/hooks/pretooluse-*.json).
const eventPreToolUse = "PreToolUse"

// sceneFiles are the extensions of the files only the scene lane writes: scenes, prefabs, assets
// and .meta files, whose references and GUIDs break when two writers change them at once
// (docs/design.md, "Lanes: scripts in parallel, scenes one at a time, `.meta` never regenerated").
// Lanes go by extension alone (A30).
var sceneFiles = []string{".unity", ".prefab", ".asset", ".meta"}

// Decision is the PreToolUse hook's answer: deny the tool call, with the reason Claude sees, or,
// with Deny false, let it through.
type Decision struct {
	Deny   bool
	Reason string
}

// Output is what the hook prints for d: for a denial, Claude Code's PreToolUse decision, a
// hookSpecificOutput whose permissionDecision is deny ([Claude Code hooks]); for anything else,
// nothing, which lets the call go on through the session's own permissions.
//
// [Claude Code hooks]: https://code.claude.com/docs/en/hooks
func (d Decision) Output() ([]byte, error) {
	if !d.Deny {
		return nil, nil
	}
	return json.Marshal(map[string]any{"hookSpecificOutput": map[string]string{
		"hookEventName":            eventPreToolUse,
		"permissionDecision":       "deny",
		"permissionDecisionReason": d.Reason,
	}})
}

// LaneGuard is the PreToolUse hook on Edit, Write and NotebookEdit: it denies a write to a scene,
// prefab, asset or .meta file in a slot whose lease is in the script lane, and lets everything
// else through, paths outside every slot included (docs/design.md, "Lanes: scripts in parallel,
// scenes one at a time, `.meta` never regenerated"). A slot with no lease, or a lease from before
// lanes, is in the script lane, the default. It reads state only for a file of those kinds. An
// error is returned for the dispatcher to fail the hook with, and the registration's
// `"onFailure": "block"` makes a failed hook deny rather than silently allow. Shell commands that
// write files are not seen: the documented hole stays open.
func LaneGuard(env Env, req Request) (Decision, error) {
	path := req.ToolInput.FilePath
	if path == "" {
		path = req.ToolInput.NotebookPath
	}
	if path == "" || !slices.Contains(sceneFiles, strings.ToLower(filepath.Ext(path))) {
		return Decision{}, nil
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(req.Cwd, path)
	}
	s, ok, err := slotContaining(env.Home, path)
	if err != nil || !ok {
		return Decision{}, err
	}
	if s.Lease != nil && s.Lease.Lane == pool.LaneScene {
		return Decision{}, nil
	}
	rel, err := filepath.Rel(s.Path, path)
	if err != nil {
		rel = path
	}
	return Decision{Deny: true, Reason: fmt.Sprintf("upool: %s is in the script lane, which does "+
		"not write %s: scenes, prefabs, assets and .meta files are written in the scene lane, by "+
		"one lease of the project at a time. To edit it, take a slot in the scene lane with "+
		"`upool take <branch> --lane scene` and work there.", s.Name, rel)}, nil
}

// slotContaining returns the slot of a registered pool that path is in, by canonical key, and
// whether there is one.
func slotContaining(home, path string) (pool.Slot, bool, error) {
	key, err := paths.Key(path)
	if err != nil {
		return pool.Slot{}, false, err
	}
	repos, err := registry.List(home)
	if err != nil {
		return pool.Slot{}, false, err
	}
	for _, repo := range repos {
		p, err := pool.Load(home, repo.Key)
		if err != nil {
			return pool.Slot{}, false, err
		}
		for _, s := range p.Slots {
			sk, err := paths.Key(s.Path)
			if err != nil {
				return pool.Slot{}, false, err
			}
			if paths.Within(key, sk) {
				return s, true, nil
			}
		}
	}
	return pool.Slot{}, false, nil
}
