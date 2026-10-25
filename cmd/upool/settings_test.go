package main

import (
	"encoding/json"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The tests of the registration init writes into a project's .claude/settings.local.json (A26),
// on a file in a temporary directory.

// testUpool is the path the tests' registrations run upool by.
const testUpool = "/opt/homebrew/bin/upool"

// otherSettings is a project settings file with other keys and another tool's hooks, some on the
// events upool registers. Its env value holds characters encoding/json escapes by default.
const otherSettings = `{
  "$schema": "https://json.schemastore.org/claude-code-settings.json",
  "model": "opus",
  "cleanupPeriodDays": 30,
  "permissions": {"allow": ["Bash(git status)"], "deny": []},
  "env": {"OTHER_TOOL": "<on> & off"},
  "hooks": {
    "PreToolUse": [
      {"matcher": "Bash", "hooks": [{"type": "command", "command": "other-tool guard"}]}
    ],
    "SessionStart": [
      {"matcher": "startup", "hooks": [{"type": "command", "command": "other-tool start", "timeout": 30}]}
    ],
    "WorktreeCreate": [
      {"hooks": [{"type": "command", "command": "/opt/other/worktree.sh"}]}
    ]
  },
  "statusLine": {"type": "command", "command": "~/.claude/statusline.sh"}
}
`

// upoolEntries replaces each "@<hook>@" in doc, a JSON document, with upool's matcher group for
// that hook: one handler, in exec form, running `upool hook <hook>` by the path upool, and for
// pre-tool-use the matcher Edit|Write|NotebookEdit and "onFailure": "block".
func upoolEntries(t *testing.T, upool, doc string) string {
	t.Helper()
	for _, h := range hookEvents {
		handler := map[string]any{"type": "command", "command": upool,
			"args": []string{"hook", h.hook}}
		group := map[string]any{"hooks": []any{handler}}
		if h.hook == "pre-tool-use" {
			handler["onFailure"] = "block"
			group["matcher"] = "Edit|Write|NotebookEdit"
		}
		doc = strings.ReplaceAll(doc, `"@`+h.hook+`@"`, string(mustJSON(t, group)))
	}
	return doc
}

// events are the events `upool hook` handles, sorted.
func events() []string {
	return slices.Sorted(maps.Keys(hookTable))
}

// settingsFile returns the path of a project's .claude/settings.local.json in a new temporary
// directory, which has no .claude yet.
func settingsFile(t *testing.T) string {
	t.Helper()
	return filepath.Join(tempDir(t), ".claude", "settings.local.json")
}

// writeSettings writes doc to file with mode, creating its directory.
func writeSettings(t *testing.T, file, doc string, mode fs.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(doc), mode); err != nil {
		t.Fatal(err)
	}
}

// checkMode fails the test unless path's permission bits are want.
func checkMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Errorf("%s mode = %v, want %v", path, got, want)
	}
}

// register registers upool's hooks in file, as init does, and returns what that did.
func register(t *testing.T, file string) installed {
	t.Helper()
	reg, err := planRegistration(file, testUpool)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reg.apply()
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// checkSettings fails the test unless the JSON document in file holds the same value as want.
func checkSettings(t *testing.T, file, want string) {
	t.Helper()
	got, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if !sameJSON(got, []byte(want)) {
		t.Errorf("%s =\n%s\nwant, as JSON,\n%s", file, got, want)
	}
}

// An existing settings file keeps its other keys and another tool's hooks, compared as JSON, and
// its mode, and upool's four entries appear once each after two registrations; the second
// changes nothing.
func TestRegistrationKeepsOtherSettings(t *testing.T) {
	file := settingsFile(t)
	writeSettings(t, file, otherSettings, 0o644)
	if got := register(t, file); got != (installed{file, updated}) {
		t.Errorf("first: %+v, want %s updated", got, file)
	}
	if got := register(t, file); got != (installed{file, unchanged}) {
		t.Errorf("second: %+v, want %s unchanged", got, file)
	}
	checkSettings(t, file, upoolEntries(t, testUpool, `{
  "$schema": "https://json.schemastore.org/claude-code-settings.json",
  "model": "opus",
  "cleanupPeriodDays": 30,
  "permissions": {"allow": ["Bash(git status)"], "deny": []},
  "env": {"OTHER_TOOL": "<on> & off"},
  "hooks": {
    "PreToolUse": [
      {"matcher": "Bash", "hooks": [{"type": "command", "command": "other-tool guard"}]},
      "@pre-tool-use@"
    ],
    "SessionStart": [
      {"matcher": "startup", "hooks": [{"type": "command", "command": "other-tool start", "timeout": 30}]},
      "@session-start@"
    ],
    "WorktreeCreate": [
      {"hooks": [{"type": "command", "command": "/opt/other/worktree.sh"}]},
      "@worktree-create@"
    ],
    "WorktreeRemove": ["@worktree-remove@"],
    "SubagentStart": ["@subagent-start@"]
  },
  "statusLine": {"type": "command", "command": "~/.claude/statusline.sh"}
}`))
	checkMode(t, file, 0o644)
}

// The rewrite keeps the file's keys in the order written and leaves <, > and & as they were, so
// that the only difference a person sees is upool's entries.
func TestRegistrationKeepsTheSettingsLayout(t *testing.T) {
	file := settingsFile(t)
	writeSettings(t, file, otherSettings, 0o600)
	register(t, file)
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var doc, hooks jsonObject
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	want := []string{"$schema", "model", "cleanupPeriodDays", "permissions", "env", "hooks",
		"statusLine"}
	if !slices.Equal(doc.keys, want) {
		t.Errorf("keys = %q, want %q", doc.keys, want)
	}
	raw, _ := doc.get("hooks")
	if err := json.Unmarshal(raw, &hooks); err != nil {
		t.Fatal(err)
	}
	want = []string{"PreToolUse", "SessionStart", "WorktreeCreate", "WorktreeRemove",
		"SubagentStart"}
	if !slices.Equal(hooks.keys, want) {
		t.Errorf("hooks keys = %q, want %q", hooks.keys, want)
	}
	if !strings.Contains(string(b), `"<on> & off"`) {
		t.Errorf("settings = %s, want the env value written as it was", b)
	}
}

// With no settings file, the registration creates one holding only the hooks, private to the
// user, in a .claude it creates, and keeps no copy of anything beside it.
func TestRegistrationCreatesTheFile(t *testing.T) {
	file := settingsFile(t)
	if got := register(t, file); got != (installed{file, created}) {
		t.Errorf("register = %+v, want %s created", got, file)
	}
	checkSettings(t, file, upoolEntries(t, testUpool, `{"hooks": {
  "WorktreeCreate": ["@worktree-create@"],
  "WorktreeRemove": ["@worktree-remove@"],
  "SessionStart": ["@session-start@"],
  "SubagentStart": ["@subagent-start@"],
  "PreToolUse": ["@pre-tool-use@"]
}}`))
	checkMode(t, file, settingsMode)
	checkMode(t, filepath.Dir(file), agentDirMode)
	entries, err := os.ReadDir(filepath.Dir(file))
	if err != nil || len(entries) != 1 {
		t.Errorf("%s holds %v, %v; want the settings file alone", filepath.Dir(file), entries, err)
	}
}

// upool's earlier entries, in exec or shell form and whichever upool they run, are replaced where
// the first of them was, not duplicated, and an event left with no handlers goes; a handler that
// runs another program, or upool but not `upool hook`, is not upool's.
func TestRegistrationReplacesItsEarlierEntries(t *testing.T) {
	file := settingsFile(t)
	writeSettings(t, file, upoolEntries(t, testUpool, `{"hooks": {
  "WorktreeCreate": [
    {"hooks": [{"type": "command", "command": "first"}]},
    {"hooks": [{"type": "command", "command": "/old/bin/upool", "args": ["hook", "worktree-create"]}]},
    {"hooks": [{"type": "command", "command": "last"}]}
  ],
  "SessionStart": [
    {"matcher": "startup", "hooks": [
      {"type": "command", "command": "other-tool start"},
      {"type": "command", "command": "upool hook session-start", "timeout": 5}
    ]},
    "@session-start@"
  ],
  "Stop": [{"hooks": [{"type": "command", "command": "/opt/homebrew/bin/upool", "args": ["hook", "stop"]}]}],
  "WorktreeRemove": [{"hooks": [
    {"type": "command", "command": "/opt/other/upool-wrapper", "args": ["hook", "worktree-remove"]},
    {"type": "command", "command": "/opt/homebrew/bin/upool", "args": ["status"]}
  ]}],
  "Notification": "not an array"
}}`), 0o600)
	if got := register(t, file); got.Change != updated {
		t.Errorf("change = %q, want %q", got.Change, updated)
	}
	checkSettings(t, file, upoolEntries(t, testUpool, `{"hooks": {
  "WorktreeCreate": [
    {"hooks": [{"type": "command", "command": "first"}]},
    "@worktree-create@",
    {"hooks": [{"type": "command", "command": "last"}]}
  ],
  "SessionStart": [
    "@session-start@",
    {"matcher": "startup", "hooks": [{"type": "command", "command": "other-tool start"}]}
  ],
  "WorktreeRemove": [
    {"hooks": [
      {"type": "command", "command": "/opt/other/upool-wrapper", "args": ["hook", "worktree-remove"]},
      {"type": "command", "command": "/opt/homebrew/bin/upool", "args": ["status"]}
    ]},
    "@worktree-remove@"
  ],
  "Notification": "not an array",
  "SubagentStart": ["@subagent-start@"],
  "PreToolUse": ["@pre-tool-use@"]
}}`))
}

// runsUpoolHook takes a handler's form from whether it has args: in exec form the command is one
// path, spaces and all, and in shell form its first word.
func TestRunsUpoolHook(t *testing.T) {
	for _, tt := range []struct {
		h    commandHook
		want bool
	}{
		{commandHook{Command: "/My Tools/upool", Args: []string{"hook", "worktree-create"}}, true},
		{commandHook{Command: "/My Tools/upool", Args: []string{}}, false},
		{commandHook{Command: "upool hook worktree-create"}, true},
		{commandHook{Command: "upool hook worktree-create", Args: []string{}}, false},
		{commandHook{Command: "/opt/homebrew/bin/upool  hook"}, true},
		{commandHook{Command: "upool hook", Args: []string{"worktree-create"}}, false},
		{commandHook{Command: "upool status"}, false},
		{commandHook{Command: ""}, false},
		{commandHook{Command: "   "}, false},
	} {
		if got := runsUpoolHook(tt.h); got != tt.want {
			t.Errorf("runsUpoolHook(%+v) = %v, want %v", tt.h, got, tt.want)
		}
	}
}

// A settings file that is a link, as a dotfiles repository leaves it, stays a link: the file it
// links to gets the registration.
func TestRegistrationWritesThroughALink(t *testing.T) {
	file := settingsFile(t)
	target := filepath.Join(tempDir(t), "claude-settings.json")
	if err := os.WriteFile(target, []byte(otherSettings), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, file); err != nil {
		t.Fatal(err)
	}
	if got := register(t, file); got != (installed{file, updated}) {
		t.Errorf("register = %+v, want %s updated", got, file)
	}
	if link, err := os.Readlink(file); err != nil || link != target {
		t.Errorf("%s links to %q, %v, want %q", file, link, err, target)
	}
	if b, err := os.ReadFile(target); err != nil || !strings.Contains(string(b), testUpool) {
		t.Errorf("%s = %s, %v; want the hooks registered in it", target, b, err)
	}
}

// A settings file that cannot be parsed, or whose hooks are not where Claude Code keeps them, is
// refused before anything is written, and left as it was.
func TestRegistrationRefusesWhatItCannotParse(t *testing.T) {
	for name, doc := range map[string]string{
		"not JSON":              `{"hooks": `,
		"not an object":         `[]`,
		"hooks not an object":   `{"hooks": ["WorktreeCreate"]}`,
		"an event not an array": `{"hooks": {"WorktreeCreate": {"hooks": []}}}`,
		"trailing data":         `{} {}`,
	} {
		t.Run(name, func(t *testing.T) {
			file := settingsFile(t)
			writeSettings(t, file, doc, 0o600)
			if _, err := planRegistration(file, testUpool); err == nil ||
				!strings.Contains(err.Error(), file) {
				t.Errorf("planRegistration = %v, want an error naming %s", err, file)
			}
			if b, err := os.ReadFile(file); err != nil || string(b) != doc {
				t.Errorf("%s = %q, %v, want it unchanged", file, b, err)
			}
		})
	}
}

// Every event `upool hook` handles is registered for a Claude Code event, and nothing else is.
func TestHookEventsNameEveryHook(t *testing.T) {
	var registered []string
	for _, h := range hookEvents {
		registered = append(registered, h.hook)
	}
	slices.Sort(registered)
	if !slices.Equal(registered, events()) {
		t.Errorf("hookEvents registers %q, want the hooks upool handles, %q", registered, events())
	}
}
