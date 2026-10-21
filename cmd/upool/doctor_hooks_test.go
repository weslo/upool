package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/weslo/upool/internal/output"
)

// The acceptance test of #69, as #163 left it: after a fresh setup the hook checks pass, and an
// upool under a Documents folder is fine; a registered upool that no longer exists, one that is not
// executable and a missing settings entry each fail, as does every other way the registration can
// be wrong; and a registered repository whose own settings register upool's hooks warns.
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
			name: "upool in Documents",
			breakIt: func(t *testing.T, p harnessPool) {
				moved := filepath.Join(tempDir(t), "Documents", "upool", "bin", upoolName)
				if err := os.MkdirAll(filepath.Dir(moved), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(registeredUpool(t, p), moved); err != nil {
					t.Fatal(err)
				}
				editSettings(t, p, func(hooks map[string]any) {
					for _, h := range hookEvents {
						hooks[h.event] = []any{map[string]any{"hooks": []any{map[string]any{
							"type": "command", "command": moved,
							"args": []string{"hook", h.hook}}}}}
					}
				})
			},
		},
		{
			name: "registered upool gone",
			breakIt: func(t *testing.T, p harnessPool) {
				if err := os.Remove(registeredUpool(t, p)); err != nil {
					t.Fatal(err)
				}
			},
			check: checkHookSettings, result: checkFail,
			reason: ", which does not exist: run upool setup as the upool on PATH",
		},
		{
			name: "registered upool not executable",
			breakIt: func(t *testing.T, p harnessPool) {
				upool := registeredUpool(t, p)
				if err := os.Remove(upool); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(upool, []byte("not a binary\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			check: checkHookSettings, result: checkFail,
			reason: ", which is not an executable file",
		},
		{
			name: "missing settings entry",
			breakIt: func(t *testing.T, p harnessPool) {
				editSettings(t, p, func(hooks map[string]any) { delete(hooks, "WorktreeRemove") })
			},
			check: checkHookSettings, result: checkFail,
			reason: "does not register upool hook worktree-remove for WorktreeRemove: run upool " +
				"setup",
		},
		{
			name: "entry for another hook",
			breakIt: func(t *testing.T, p harnessPool) {
				editSettings(t, p, func(hooks map[string]any) {
					hooks["SessionStart"] = []any{map[string]any{"hooks": []any{map[string]any{
						"type": "command", "command": registeredUpool(t, p),
						"args": []string{"hook", "worktree-create"}}}}}
				})
			},
			check: checkHookSettings, result: checkFail,
			reason: "does not register upool hook session-start for SessionStart",
		},
		{
			name: "relative upool",
			breakIt: func(t *testing.T, p harnessPool) {
				editSettings(t, p, func(hooks map[string]any) {
					hooks["SessionStart"] = []any{map[string]any{"hooks": []any{map[string]any{
						"type": "command", "command": upoolName,
						"args": []string{"hook", "session-start"}}}}}
				})
			},
			check: checkHookSettings, result: checkFail,
			reason: "does not register upool hook session-start for SessionStart",
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
			name: "project settings register upool",
			breakIt: func(t *testing.T, p harnessPool) {
				p.WriteFile(".claude/settings.local.json", `{"hooks": {"WorktreeCreate": [`+
					`{"hooks": [{"type": "command", "command": "/opt/homebrew/bin/upool", `+
					`"args": ["hook", "worktree-create"]}]}]}}`)
			},
			check: checkHookProvenance, result: checkWarn,
			reason: "settings.local.json registers upool's hooks",
		},
		{
			name: "project settings run upool hook",
			breakIt: func(t *testing.T, p harnessPool) {
				p.WriteFile(".claude/settings.json", `{"hooks": {"SessionStart": [`+
					`{"hooks": [{"type": "command", "command": "upool hook session-start"}]}]}}`)
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

// registeredUpool returns the upool p's settings.json registers for WorktreeCreate, as setup
// wrote it.
func registeredUpool(t *testing.T, p harnessPool) string {
	t.Helper()
	b, err := os.ReadFile(settingsPath(p.Home))
	if err != nil {
		t.Fatal(err)
	}
	registered, err := registeredHooks(b)
	if err != nil {
		t.Fatal(err)
	}
	if h := registered["WorktreeCreate"]; len(h) == 1 && filepath.IsAbs(h[0].Command) {
		return h[0].Command
	}
	t.Fatalf("%s registers %+v for WorktreeCreate, want setup's one handler",
		settingsPath(p.Home), registered["WorktreeCreate"])
	return ""
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
