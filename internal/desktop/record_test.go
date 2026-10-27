package desktop

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeRecord writes content as the app's record under home.
func writeRecord(t *testing.T, home, content string) {
	t.Helper()
	path := filepath.Join(home, filepath.FromSlash(RecordFile))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The record names a worktree by its path, compared as paths.Key compares paths, whatever its
// leasedBy, and names nothing else (A50; docs/evidence/session-end-probe).
func TestLoadFindsTheWorktreesTheAppRecords(t *testing.T) {
	home := t.TempDir()
	slot := filepath.Join(home, "repo", ".claude", "worktrees", "slot-4")
	writeRecord(t, home, `{"schemaVersion": 2, "originPins": {}, "worktrees": {
		"pwd-03f0cb": {"name": "pwd-03f0cb", "path": "`+slot+`",
			"leasedBy": "local_1e55923b-a88c-428b-8fae-2fd629d6dbdd", "leaseCleared": null},
		"pwd-f8ad8e": {"name": "pwd-f8ad8e", "path": "`+filepath.Join(home, "other")+`",
			"leasedBy": null},
		"unnamed": {"path": "`+filepath.Join(home, "unnamed")+`"},
		"by-key": {"name": "by-name", "path": "`+filepath.Join(home, "named")+`"},
		"relative": {"name": "relative", "path": "repo/x"}}}`)
	r := Load(home)
	if !r.Known || r.Why != "" {
		t.Fatalf("Load = %+v; want it known", r)
	}
	const session = "local_1e55923b-a88c-428b-8fae-2fd629d6dbdd"
	for _, tt := range []struct {
		path string
		want Worktree
		says string
	}{
		{slot, Worktree{"pwd-03f0cb", session}, "session " + session + ", as its worktree pwd-03f0cb"},
		{slot + string(filepath.Separator), Worktree{"pwd-03f0cb", session}, ""},
		{filepath.Join(home, "other"), Worktree{"pwd-f8ad8e", ""}, "a session, as its worktree pwd-f8ad8e"},
		{filepath.Join(home, "unnamed"), Worktree{"unnamed", ""}, ""},
		{filepath.Join(home, "named"), Worktree{"by-name", ""}, ""},
		{filepath.Join(home, "repo", ".claude", "worktrees", "slot-5"), Worktree{}, ""},
		{filepath.Join(home, "repo", "x"), Worktree{}, ""},
	} {
		got, ok := r.Holds(tt.path)
		if got != tt.want || ok != (tt.want != Worktree{}) {
			t.Errorf("Holds(%s) = %+v, %t; want %+v", tt.path, got, ok, tt.want)
		}
		if tt.says != "" && got.String() != tt.says {
			t.Errorf("Holds(%s) says %q, want %q", tt.path, got, tt.says)
		}
	}
}

// A path the record names in another case, as the app may write it, is the same path on a
// case-insensitive volume, the Mac's default.
func TestHoldsComparesPathsAsKeys(t *testing.T) {
	home := t.TempDir()
	slot := filepath.Join(home, "Repo", "slot-1")
	if err := os.MkdirAll(slot, 0o755); err != nil {
		t.Fatal(err)
	}
	writeRecord(t, home, `{"worktrees": {"w": {"name": "w", "path": "`+strings.ToLower(slot)+`"}}}`)
	if _, err := os.Stat(strings.ToLower(slot)); err != nil {
		t.Skip("a case-sensitive volume")
	}
	if got, ok := Load(home).Holds(slot); !ok || got.Name != "w" {
		t.Errorf("Holds = %+v, %t; want w", got, ok)
	}
}

// A record that is missing, unreadable or in a shape upool does not know is unknown, says why,
// and names nothing.
func TestLoadReadsAnUnknownRecordAsUnknown(t *testing.T) {
	for _, tt := range []struct {
		name, content, why string
	}{
		{"missing", "", "does not exist"},
		{"not JSON", "{", "unexpected end of JSON input"},
		{"no worktrees", `{"schemaVersion": 2}`, "has no worktrees object"},
		{"worktrees not an object", `{"worktrees": []}`, "cannot unmarshal array"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			if tt.content != "" {
				writeRecord(t, home, tt.content)
			}
			r := Load(home)
			if r.Known || !strings.Contains(r.Why, tt.why) {
				t.Errorf("Load = %+v; want unknown, saying %q", r, tt.why)
			}
			if _, ok := r.Holds(filepath.Join(home, "x")); ok {
				t.Error("an unknown record holds a path")
			}
		})
	}
}
