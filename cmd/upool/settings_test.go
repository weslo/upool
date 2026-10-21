package main

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/weslo/upool/internal/output"
)

// otherSettings is a user settings.json with other keys and another tool's hooks, some on the
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

// upoolEntries replaces each "@<event>@" in doc, a JSON document, with upool's matcher group for
// that event: one handler, in exec form, running `upool hook <event>` by the path upool.
func upoolEntries(t *testing.T, upool, doc string) string {
	t.Helper()
	for _, event := range events() {
		group := mustJSON(t, map[string]any{"hooks": []any{map[string]any{
			"type": "command", "command": upool, "args": []string{"hook", event},
		}}})
		doc = strings.ReplaceAll(doc, `"@`+event+`@"`, string(group))
	}
	return doc
}

// writeSettings writes doc as e's ~/.claude/settings.json with mode.
func writeSettings(t *testing.T, e setupEnv, doc string, mode fs.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(settingsPath(e.home)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath(e.home), []byte(doc), mode); err != nil {
		t.Fatal(err)
	}
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

// The acceptance test of #67: an existing settings.json keeps its other keys and another tool's
// hooks, compared as JSON, and upool's three entries appear once each after two runs.
func TestSetupRegistersHooks(t *testing.T) {
	e := newSetupEnv(t)
	writeSettings(t, e, otherSettings, 0o644)

	setupData(t, e.setup(t, "--json"))
	got := setupData(t, e.setup(t, "--json"))
	if want := (installed{settingsPath(e.home), unchanged}); got.Settings != want {
		t.Errorf("second run: settings = %+v, want %+v", got.Settings, want)
	}

	checkSettings(t, settingsPath(e.home), upoolEntries(t, e.upool, `{
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
      {"matcher": "startup", "hooks": [{"type": "command", "command": "other-tool start", "timeout": 30}]},
      "@session-start@"
    ],
    "WorktreeCreate": [
      {"hooks": [{"type": "command", "command": "/opt/other/worktree.sh"}]},
      "@worktree-create@"
    ],
    "WorktreeRemove": ["@worktree-remove@"]
  },
  "statusLine": {"type": "command", "command": "~/.claude/statusline.sh"}
}`))
	checkMode(t, settingsPath(e.home), 0o644)
	checkSettings(t, settingsPath(e.home)+backupSuffix, otherSettings)
}

// The rewrite keeps the file's keys in the order written and leaves <, > and & as they were, so
// that the only difference a person sees is upool's entries.
func TestSetupKeepsTheSettingsLayout(t *testing.T) {
	e := newSetupEnv(t)
	writeSettings(t, e, otherSettings, 0o600)
	setupData(t, e.setup(t, "--json"))

	b, err := os.ReadFile(settingsPath(e.home))
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
	want = []string{"PreToolUse", "SessionStart", "WorktreeCreate", "WorktreeRemove"}
	if !slices.Equal(hooks.keys, want) {
		t.Errorf("hooks keys = %q, want %q", hooks.keys, want)
	}
	if !strings.Contains(string(b), `"<on> & off"`) {
		t.Errorf("settings.json = %s, want the env value written as it was", b)
	}
}

// With no settings.json, setup creates one holding only the hooks, private to the user, and
// keeps no copy, since there was nothing to keep. It writes nothing into the project it runs
// in: hooks registered from a project's settings are not vouched for (docs/contracts.md (a)).
func TestSetupCreatesSettings(t *testing.T) {
	e := newSetupEnv(t)
	project := tempDir(t)
	local := filepath.Join(project, ".claude", "settings.local.json")
	if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(local, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.env["PWD"] = project

	got := setupData(t, e.setup(t, "--json"))
	if want := (installed{settingsPath(e.home), created}); got.Settings != want ||
		got.Backup != "" {
		t.Errorf("settings = %+v, backup %q, want %+v and none", got.Settings, got.Backup, want)
	}
	checkSettings(t, settingsPath(e.home), upoolEntries(t, e.upool, `{"hooks": {
  "WorktreeCreate": ["@worktree-create@"],
  "WorktreeRemove": ["@worktree-remove@"],
  "SessionStart": ["@session-start@"]
}}`))
	checkMode(t, settingsPath(e.home), settingsMode)
	checkMode(t, filepath.Dir(settingsPath(e.home)), agentDirMode)
	if _, err := os.Lstat(settingsPath(e.home) + backupSuffix); err == nil {
		t.Errorf("setup kept a copy of a settings.json that did not exist")
	}

	var files []string
	for _, root := range []string{e.home, project} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				files = append(files, path)
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	want := []string{filepath.Join(e.home, ".claude", "settings.json"),
		filepath.Join(e.home, ".config", "upool", "config.toml")}
	want = append(append(append(want, e.skills()...), e.instructions()...), local)
	slices.Sort(files)
	slices.Sort(want)
	if !slices.Equal(files, want) {
		t.Errorf("files after setup = %q, want %q", files, want)
	}
	if b, err := os.ReadFile(local); err != nil || string(b) != "{}\n" {
		t.Errorf("%s = %q, %v, want it unchanged", local, b, err)
	}
}

// The copy is made once, of settings.json as it was before upool's first change, and a later run
// that changes the file again leaves it alone.
func TestSetupKeepsOneBackup(t *testing.T) {
	e := newSetupEnv(t)
	writeSettings(t, e, otherSettings, 0o640)
	r := e.setup(t)
	if r.code != output.ExitOK {
		t.Fatalf("exit = %d, want %d; stderr %q", r.code, output.ExitOK, r.stderr)
	}
	backup := settingsPath(e.home) + backupSuffix
	for _, line := range []string{"kept a copy of " + settingsPath(e.home) + " as " + backup,
		"updated " + settingsPath(e.home)} {
		if !strings.Contains(r.stdout, line+"\n") {
			t.Errorf("stdout = %q, want a line %q", r.stdout, line)
		}
	}
	if b, err := os.ReadFile(backup); err != nil || string(b) != otherSettings {
		t.Errorf("%s = %q, %v, want settings.json as it was", backup, b, err)
	}
	checkMode(t, backup, 0o640)

	writeSettings(t, e, `{"model": "sonnet"}`, 0o640)
	got := setupData(t, e.setup(t, "--json"))
	if want := (installed{settingsPath(e.home), updated}); got.Settings != want ||
		got.Backup != "" {
		t.Errorf("second run: settings = %+v, backup %q, want %+v and none", got.Settings,
			got.Backup, want)
	}
	if b, err := os.ReadFile(backup); err != nil || string(b) != otherSettings {
		t.Errorf("second run: %s = %q, %v, want the first copy", backup, b, err)
	}
	checkSettings(t, settingsPath(e.home), upoolEntries(t, e.upool, `{"model": "sonnet", "hooks": {
  "WorktreeCreate": ["@worktree-create@"],
  "WorktreeRemove": ["@worktree-remove@"],
  "SessionStart": ["@session-start@"]
}}`))
}

// upool's earlier entries, in exec or shell form and whichever upool they run, are replaced where
// the first of them was, not duplicated, and an event left with no handlers goes; a handler that
// runs another program, or upool but not `upool hook`, is not upool's.
func TestSetupReplacesItsEarlierEntries(t *testing.T) {
	e := newSetupEnv(t)
	writeSettings(t, e, upoolEntries(t, e.upool, `{"hooks": {
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
	got := setupData(t, e.setup(t, "--json"))
	if got.Settings.Change != updated {
		t.Errorf("settings change = %q, want %q", got.Settings.Change, updated)
	}
	checkSettings(t, settingsPath(e.home), upoolEntries(t, e.upool, `{"hooks": {
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
  "Notification": "not an array"
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

// A settings.json that is a link, as a dotfiles repository leaves it, stays a link: the file it
// links to gets the registration, and the copy goes beside the link.
func TestSetupWritesThroughALinkedSettingsFile(t *testing.T) {
	e := newSetupEnv(t)
	target := filepath.Join(tempDir(t), "claude-settings.json")
	if err := os.WriteFile(target, []byte(otherSettings), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(settingsPath(e.home)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, settingsPath(e.home)); err != nil {
		t.Fatal(err)
	}
	got := setupData(t, e.setup(t, "--json"))
	if want := (installed{settingsPath(e.home), updated}); got.Settings != want {
		t.Errorf("settings = %+v, want %+v", got.Settings, want)
	}
	if link, err := os.Readlink(settingsPath(e.home)); err != nil || link != target {
		t.Errorf("settings.json links to %q, %v, want %q", link, err, target)
	}
	b, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	var doc jsonObject
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if raw, _ := doc.get("hooks"); !strings.Contains(string(raw), e.upool) {
		t.Errorf("%s = %s, want the hooks registered in it", target, b)
	}
	checkSettings(t, settingsPath(e.home)+backupSuffix, otherSettings)
}

// A settings.json that setup cannot parse, or whose hooks are not where Claude Code keeps them,
// is refused before anything is written, and left as it was.
func TestSetupRefusesSettingsItCannotRegisterIn(t *testing.T) {
	for name, doc := range map[string]string{
		"not JSON":              `{"hooks": `,
		"not an object":         `[]`,
		"hooks not an object":   `{"hooks": ["WorktreeCreate"]}`,
		"an event not an array": `{"hooks": {"WorktreeCreate": {"hooks": []}}}`,
		"trailing data":         `{} {}`,
	} {
		t.Run(name, func(t *testing.T) {
			e := newSetupEnv(t)
			writeSettings(t, e, doc, 0o600)
			r := e.setup(t)
			if r.code != output.ExitFailure {
				t.Errorf("exit = %d, want %d", r.code, output.ExitFailure)
			}
			if !strings.Contains(r.stderr, settingsPath(e.home)) {
				t.Errorf("stderr = %q, want it to name %s", r.stderr, settingsPath(e.home))
			}
			if b, err := os.ReadFile(settingsPath(e.home)); err != nil || string(b) != doc {
				t.Errorf("settings.json = %q, %v, want it unchanged", b, err)
			}
			for _, path := range []string{filepath.Join(e.home, ".upool"),
				settingsPath(e.home) + backupSuffix} {
				if _, err := os.Lstat(path); err == nil {
					t.Errorf("%s exists after a refusal", path)
				}
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
