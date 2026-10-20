package main

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/weslo/upool/assets"
	"github.com/weslo/upool/internal/output"
)

// hookScript is the path of the hook script for event on p's machine.
func (p harnessPool) hookScript(event string) string {
	return filepath.Join(p.Home, ".upool", "hooks", event+".sh")
}

// The acceptance test of #69: after a fresh setup the hook checks pass; a deleted script, a
// written path that no longer exists and a missing settings entry each fail, as does every other
// way a script or the registration can be wrong; and a registered repository whose own settings
// register upool's hooks warns.
func TestDoctorHooks(t *testing.T) {
	tests := []struct {
		name string
		// breakIt changes p's machine after setup; a nil one leaves it as setup made it.
		breakIt func(t *testing.T, p harnessPool)
		check   string
		result  checkResult
		reason  string
	}{
		{name: "fresh setup"},
		{
			name: "deleted script",
			breakIt: func(t *testing.T, p harnessPool) {
				if err := os.Remove(p.hookScript("worktree-remove")); err != nil {
					t.Fatal(err)
				}
			},
			check: checkHookScripts, result: checkFail,
			reason: "worktree-remove.sh is missing: run upool setup",
		},
		{
			name: "written path gone",
			breakIt: func(t *testing.T, p harnessPool) {
				if err := os.Remove(scriptedUpool(t, p, "worktree-create")); err != nil {
					t.Fatal(err)
				}
			},
			check: checkHookScripts, result: checkFail,
			reason: ", which does not exist: run upool setup as the upool on PATH",
		},
		{
			name: "missing settings entry",
			breakIt: func(t *testing.T, p harnessPool) {
				editSettings(t, p, func(hooks map[string]any) { delete(hooks, "WorktreeRemove") })
			},
			check: checkHookSettings, result: checkFail,
			reason: "does not register worktree-remove.sh for WorktreeRemove: run upool setup",
		},
		{
			name: "entry for another script",
			breakIt: func(t *testing.T, p harnessPool) {
				editSettings(t, p, func(hooks map[string]any) {
					hooks["SessionStart"] = []any{map[string]any{"hooks": []any{map[string]any{
						"type": "command", "command": p.hookScript("worktree-create")}}}}
				})
			},
			check: checkHookSettings, result: checkFail,
			reason: "does not register session-start.sh for SessionStart",
		},
		{
			name: "no settings file",
			breakIt: func(t *testing.T, p harnessPool) {
				if err := os.Remove(settingsPath(p.Home)); err != nil {
					t.Fatal(err)
				}
			},
			check: checkHookSettings, result: checkFail,
			reason: "settings.json does not exist, so no hook is registered",
		},
		{
			name: "not executable",
			breakIt: func(t *testing.T, p harnessPool) {
				if err := os.Chmod(p.hookScript("session-start"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			check: checkHookScripts, result: checkFail,
			reason: "session-start.sh is not executable",
		},
		{
			name: "edited script",
			breakIt: func(t *testing.T, p harnessPool) {
				if err := os.WriteFile(p.hookScript("worktree-create"),
					[]byte("#!/bin/sh\nexec upool hook worktree-create\n"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
			check: checkHookScripts, result: checkFail,
			reason: "worktree-create.sh is not the script upool setup writes",
		},
		{
			name: "scripts in Documents",
			breakIt: func(t *testing.T, p harnessPool) {
				hooks := filepath.Dir(p.hookScript("worktree-create"))
				moved := filepath.Join(tempDir(t), "Documents", "hooks")
				if err := os.MkdirAll(filepath.Dir(moved), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(hooks, moved); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(moved, hooks); err != nil {
					t.Fatal(err)
				}
			},
			check: checkHookScripts, result: checkFail,
			reason: "which the desktop app's hook helper cannot read from",
		},
		{
			name: "project settings register a script",
			breakIt: func(t *testing.T, p harnessPool) {
				p.WriteFile(".claude/settings.local.json", `{"hooks": {"WorktreeCreate": [`+
					`{"hooks": [{"type": "command", "command": "~/.upool/hooks/worktree-create.sh"}]}]}}`)
			},
			check: checkHookProvenance, result: checkWarn,
			reason: "settings.local.json registers upool's hooks",
		},
		{
			name: "project settings run upool hook",
			breakIt: func(t *testing.T, p harnessPool) {
				p.WriteFile(".claude/settings.json", `{"hooks": {"SessionStart": [`+
					`{"hooks": [{"type": "command", "command": "upool", "args": ["hook", "session-start"]}]}]}}`)
			},
			check: checkHookProvenance, result: checkWarn,
			reason: "settings.json registers upool's hooks, which ~/.claude/settings.json " +
				"already registers for every repository",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newHarnessPool(t)
			if tt.breakIt != nil {
				tt.breakIt(t, p)
			}
			r := runIn(t, p.Env, p.Repo, "doctor", "--json")
			if want := output.ExitOK; tt.result == checkFail {
				if r.code != output.ExitFailure {
					t.Errorf("exit = %d, want %d", r.code, output.ExitFailure)
				}
			} else if r.code != want {
				t.Errorf("exit = %d, want %d; stderr %q", r.code, want, r.stderr)
			}
			p.checkResults(t, doctorChecks(t, r), tt.check, tt.result, tt.reason)
		})
	}
}

// scriptedUpool returns the upool p's script for event runs, as setup wrote it.
func scriptedUpool(t *testing.T, p harnessPool, event string) string {
	t.Helper()
	script, err := os.ReadFile(p.hookScript(event))
	if err != nil {
		t.Fatal(err)
	}
	template, err := fs.ReadFile(assets.Hooks, "hooks/"+event+".sh")
	if err != nil {
		t.Fatal(err)
	}
	upool, ok := scriptUpool(string(script), string(template))
	if !ok {
		t.Fatalf("%s is not the script setup writes: %q", p.hookScript(event), script)
	}
	return upool
}

// editSettings changes the hooks in p's ~/.claude/settings.json with f.
func editSettings(t *testing.T, p harnessPool, f func(hooks map[string]any)) {
	t.Helper()
	b, err := os.ReadFile(settingsPath(p.Home))
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	if err := json.Unmarshal(b, &settings); err != nil {
		t.Fatal(err)
	}
	f(settings["hooks"].(map[string]any))
	if err := os.WriteFile(settingsPath(p.Home), mustJSON(t, settings), 0o600); err != nil {
		t.Fatal(err)
	}
}

// shellUnquote reads back what shellQuote writes, a path with a single quote in it included, and
// refuses what shellQuote would not write; scriptUpool takes only an absolute path.
func TestShellUnquoteReversesShellQuote(t *testing.T) {
	for _, s := range []string{"/opt/homebrew/bin/upool", "/tmp/it's here/upool", "", "'"} {
		if got, ok := shellUnquote(shellQuote(s)); !ok || got != s {
			t.Errorf("shellUnquote(shellQuote(%q)) = %q, %v; want %q, true", s, got, ok, s)
		}
	}
	for _, q := range []string{"upool", "'a'b'", "'a", `"a"`} {
		if got, ok := shellUnquote(q); ok {
			t.Errorf("shellUnquote(%q) = %q, true; want false", q, got)
		}
	}
	template := "exec " + assets.HookUpool + " hook x\n"
	if got, ok := scriptUpool("exec 'bin/upool' hook x\n", template); ok {
		t.Errorf("scriptUpool with a relative path = %q, true; want false", got)
	}
	if got, ok := scriptUpool("exec '/bin/upool' hook x\n", template); !ok || got != "/bin/upool" {
		t.Errorf("scriptUpool = %q, %v; want /bin/upool, true", got, ok)
	}
	if _, ok := scriptUpool("exec '/bin/upool' hook y\n", template); ok {
		t.Error("scriptUpool accepted a script for another event")
	}
}
