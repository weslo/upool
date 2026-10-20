package pool

import (
	"os"
	"strings"
	"testing"

	"github.com/weslo/upool/internal/git"
	"github.com/weslo/upool/internal/testenv"
)

// The scriptingDefineSymbols entry of testdata/captures/git/projectsettings-define-reorder.diff,
// as it was and as the Editor rewrote it: the same set, in another order. testenv's
// ProjectSettings.asset has the first. The tests rebuild the capture's state with real git rather
// than parse it (testdata/README.md, captures/git/).
const (
	capturedBefore = "    Standalone: SENTIS_ANALYTICS_ENABLED;APP_UI_EDITOR_ONLY;STEAMWORKS_NET"
	capturedAfter  = "    Standalone: APP_UI_EDITOR_ONLY;STEAMWORKS_NET;SENTIS_ANALYTICS_ENABLED"
)

// noiseEnv is a testenv repository and a git client for it.
func noiseEnv(t *testing.T) (*testenv.Env, git.Client) {
	t.Helper()
	e := testenv.New(t)
	return e, git.Client{Runner: git.Exec{}, Env: e.Environ()}
}

// editSettings replaces old with new in e's ProjectSettings.asset, which must contain it.
func editSettings(t *testing.T, e *testenv.Env, old, new string) {
	t.Helper()
	doc := readRepoFile(t, e, SettingsAsset)
	if !strings.Contains(doc, old) {
		t.Fatalf("%s has no %q", SettingsAsset, old)
	}
	e.WriteFile(SettingsAsset, strings.Replace(doc, old, new, 1))
}

// readRepoFile returns name, relative to e's repository.
func readRepoFile(t *testing.T, e *testenv.Env, name string) string {
	t.Helper()
	b, err := os.ReadFile(e.Path(name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestNoiseIsTheCapturedReorderAndIsReverted(t *testing.T) {
	e, g := noiseEnv(t)
	committed := readRepoFile(t, e, SettingsAsset)
	editSettings(t, e, capturedBefore, capturedAfter)
	diff := e.Git("diff", "-U0", "--", SettingsAsset)
	if !strings.Contains(diff, "\n-"+capturedBefore+"\n+"+capturedAfter) {
		t.Fatalf("git diff =\n%s\nwant the capture's two lines", diff)
	}

	noise, err := Noise(t.Context(), g, e.Repo)
	if err != nil || !noise {
		t.Fatalf("Noise = %v, %v, want true", noise, err)
	}
	reverted, err := RevertNoise(t.Context(), g, e.Repo)
	if err != nil || !reverted {
		t.Fatalf("RevertNoise = %v, %v, want true", reverted, err)
	}
	if got := readRepoFile(t, e, SettingsAsset); got != committed {
		t.Errorf("%s =\n%s\nwant it as committed:\n%s", SettingsAsset, got, committed)
	}
	if status := e.Git("status", "--porcelain"); status != "" {
		t.Errorf("status = %q, want nothing", status)
	}
	if noise, err := Noise(t.Context(), g, e.Repo); err != nil || noise {
		t.Errorf("Noise after the revert = %v, %v, want false", noise, err)
	}
}

// Nothing but the reorder is noise, and RevertNoise leaves anything else as it was.
func TestNoiseIsNotAnyOtherChange(t *testing.T) {
	reorder := func(t *testing.T, e *testenv.Env) {
		editSettings(t, e, capturedBefore, capturedAfter)
	}
	editScript := func(t *testing.T, e *testenv.Env) {
		e.WriteFile(testenv.GameScript, readRepoFile(t, e, testenv.GameScript)+"// edited\n")
	}
	tests := []struct {
		name   string
		change func(t *testing.T, e *testenv.Env)
	}{
		{"nothing changed", func(*testing.T, *testenv.Env) {}},
		{"a define added", func(t *testing.T, e *testenv.Env) {
			editSettings(t, e, capturedBefore, capturedBefore+";EXTRA_DEFINE")
		}},
		{"a define removed", func(t *testing.T, e *testenv.Env) {
			editSettings(t, e, capturedBefore,
				"    Standalone: SENTIS_ANALYTICS_ENABLED;APP_UI_EDITOR_ONLY")
		}},
		{"a define replaced", func(t *testing.T, e *testenv.Env) {
			editSettings(t, e, capturedBefore,
				"    Standalone: SENTIS_ANALYTICS_ENABLED;APP_UI_EDITOR_ONLY;OTHER_DEFINE")
		}},
		{"a reorder and another changed line", func(t *testing.T, e *testenv.Env) {
			reorder(t, e)
			editSettings(t, e, "webWasm2023: 0", "webWasm2023: 1")
		}},
		{"a reorder and a change in another file", func(t *testing.T, e *testenv.Env) {
			reorder(t, e)
			editScript(t, e)
		}},
		{"a reorder and an untracked file", func(t *testing.T, e *testenv.Env) {
			reorder(t, e)
			e.WriteFile(testenv.ScratchScript, "// scratch\n")
		}},
		{"a change in another file", editScript},
		{"a staged reorder", func(t *testing.T, e *testenv.Env) {
			reorder(t, e)
			e.Git("add", SettingsAsset)
		}},
		{"a staged reorder, reordered again in the worktree", func(t *testing.T, e *testenv.Env) {
			reorder(t, e)
			e.Git("add", SettingsAsset)
			editSettings(t, e, capturedAfter,
				"    Standalone: STEAMWORKS_NET;SENTIS_ANALYTICS_ENABLED;APP_UI_EDITOR_ONLY")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, g := noiseEnv(t)
			tt.change(t, e)
			settings, status := readRepoFile(t, e, SettingsAsset), e.Git("status", "--porcelain")

			if noise, err := Noise(t.Context(), g, e.Repo); err != nil || noise {
				t.Errorf("Noise = %v, %v, want false", noise, err)
			}
			if reverted, err := RevertNoise(t.Context(), g, e.Repo); err != nil || reverted {
				t.Errorf("RevertNoise = %v, %v, want false", reverted, err)
			}
			if got := readRepoFile(t, e, SettingsAsset); got != settings {
				t.Errorf("%s = %q, want it unchanged: %q", SettingsAsset, got, settings)
			}
			if got := e.Git("status", "--porcelain"); got != status {
				t.Errorf("status = %q, want it unchanged: %q", got, status)
			}
		})
	}
}

// settingsWith is a ProjectSettings.asset whose scriptingDefineSymbols mapping has entries, each a
// whole line without its line ending.
func settingsWith(entries ...string) string {
	return "PlayerSettings:\n  webWasm2023: 0\n  scriptingDefineSymbols:\n" +
		strings.Join(entries, "\n") + "\n  additionalCompilerArguments: {}\n"
}

// The rule itself, on two versions of the file: each platform's symbols compared as a multiset,
// everything else line for line.
func TestNoiseRuleComparesEachPlatformsSymbols(t *testing.T) {
	const (
		android    = "    Android: A;B"
		standalone = "    Standalone: C;D;E"
	)
	before := settingsWith(android, standalone)
	tests := []struct {
		name          string
		before, after string
		want          bool
	}{
		{"two platforms reordered", before,
			settingsWith("    Android: B;A", "    Standalone: E;C;D"), true},
		{"the platforms' lines swapped", before, settingsWith(standalone, android), true},
		{"the mapping last in the file", "a: 1\n  scriptingDefineSymbols:\n    S: A;B\n",
			"a: 1\n  scriptingDefineSymbols:\n    S: B;A\n", true},
		{"CRLF line endings", strings.ReplaceAll(before, "\n", "\r\n"),
			strings.ReplaceAll(settingsWith("    Android: B;A", standalone), "\n", "\r\n"), true},
		{"identical", before, before, false},
		{"a platform added", before, settingsWith(android, standalone, "    iOS: F"), false},
		{"a platform removed", before, settingsWith(android), false},
		{"a platform renamed", before, settingsWith("    Androidx: B;A", standalone), false},
		{"an entry re-indented", before, settingsWith("      Android: B;A", standalone), false},
		{"a symbol repeated", before, settingsWith("    Android: B;A;A", standalone), false},
		{"a symbol emptied", before, settingsWith("    Android: B;A;", standalone), false},
		{"the spacing changed", before, settingsWith("    Android:  B;A", standalone), false},
		{"another line changed", before,
			strings.Replace(settingsWith("    Android: B;A", standalone), "webWasm2023: 0",
				"webWasm2023: 1", 1), false},
		{"the final newline removed", before,
			strings.TrimSuffix(settingsWith("    Android: B;A", standalone), "\n"), false},
		{"the mapping's line changed", before,
			strings.Replace(settingsWith("    Android: B;A", standalone),
				"  scriptingDefineSymbols:", "  scriptingDefineSymbols: ", 1), false},
		{"an entry with no colon", settingsWith("    Android A;B", standalone),
			settingsWith("    Android B;A", standalone), false},
		{"a platform named twice", settingsWith(android, android),
			settingsWith("    Android: B;A", android), false},
		{"no mapping", "a: 1\n  scriptingDefineSymbols: {}\n",
			"a: 2\n  scriptingDefineSymbols: {}\n", false},
		{"two mappings", before + before,
			settingsWith("    Android: B;A", standalone) + before, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := definesReordered(tt.before, tt.after); got != tt.want {
				t.Errorf("definesReordered = %v, want %v\nbefore %q\nafter  %q", got, tt.want,
					tt.before, tt.after)
			}
		})
	}
}
