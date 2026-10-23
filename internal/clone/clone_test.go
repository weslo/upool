package clone

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/weslo/upool/internal/pool"
)

// The next name is the lowest slot-N that no slot has and whose directory is not on disk: one
// with a directory but no entry is a slot prune is removing, or one left behind.
func TestNextNameTakesTheLowestFreeNumber(t *testing.T) {
	tests := []struct {
		slots, onDisk []string
		want          string
	}{
		{nil, nil, "slot-1"},
		{[]string{"slot-1"}, nil, "slot-2"},
		{[]string{"slot-2", "slot-3"}, nil, "slot-1"},
		{[]string{"slot-1", "slot-3"}, nil, "slot-2"},
		{[]string{"slot-1"}, []string{"slot-2"}, "slot-3"},
		{nil, []string{"slot-1", "slot-2"}, "slot-3"},
	}
	for _, tt := range tests {
		var slots []pool.Slot
		for _, name := range tt.slots {
			slots = append(slots, pool.Slot{Name: name})
		}
		onDisk := func(name string) bool { return slices.Contains(tt.onDisk, name) }
		if got := nextName(slots, onDisk); got != tt.want {
			t.Errorf("nextName(%v, on disk %v) = %s, want %s", tt.slots, tt.onDisk, got, tt.want)
		}
	}
}

// Step 8 trusts a slot only when its .git file points back into the repository's
// .git/worktrees/ (docs/design.md, "Leases that survive crashes and never reclaim a live tree").
func TestCheckBacklink(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(root, "project")
	slot := filepath.Join(repo, ".claude", "worktrees", "slot-1")
	inside := filepath.Join(repo, ".git", "worktrees", "slot-1")
	tests := []struct {
		name string
		git  string // the .git file's content; "" makes .git a directory
		want string // "" when the check passes, else a part of the error
	}{
		{"absolute, inside", "gitdir: " + inside + "\n", ""},
		{"relative, inside", "gitdir: ../../../.git/worktrees/slot-1\n", ""},
		{"another repository", "gitdir: " + filepath.Join(root, "other", ".git", "worktrees",
			"slot-1") + "\n", "outside"},
		{"the worktrees directory itself", "gitdir: " + filepath.Join(repo, ".git",
			"worktrees") + "\n", "outside"},
		{"a sibling with the same prefix", "gitdir: " + filepath.Join(repo, ".git",
			"worktrees-x", "slot-1") + "\n", "outside"},
		{"no gitdir line", "ref: refs/heads/main\n", "no gitdir line"},
		{"a directory", "", "is not a file"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := os.RemoveAll(slot); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(slot, 0o755); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(slot, ".git")
			if tt.git == "" {
				err = os.Mkdir(file, 0o755)
			} else {
				err = os.WriteFile(file, []byte(tt.git), 0o644)
			}
			if err != nil {
				t.Fatal(err)
			}
			err := CheckBacklink(repo, slot)
			switch {
			case tt.want == "" && err != nil:
				t.Errorf("err = %v, want nil", err)
			case tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)):
				t.Errorf("err = %v, want one containing %q", err, tt.want)
			}
		})
	}
}
