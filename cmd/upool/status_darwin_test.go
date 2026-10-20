//go:build darwin

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/weslo/upool/internal/pool"
	"github.com/weslo/upool/internal/registry"
)

// TestStatusMatchesThePrivateVarSpellingOfAPool registers a pool under one spelling of /var and
// runs status under the other, as TestWhichMatchesThePrivateVarSpellingOfASlot does for which:
// macOS spells the directory both ways, so either spelling is inside the pool (docs/design.md,
// "Repository, CI and Homebrew distribution", the first of the five rules after the layout).
func TestStatusMatchesThePrivateVarSpellingOfAPool(t *testing.T) {
	dir, err := os.MkdirTemp("/var/tmp", "upool-status-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Error(err)
		}
	})
	if !strings.HasPrefix(dir, "/var/") {
		t.Fatalf("MkdirTemp(/var/tmp) = %q, want it under /var", dir)
	}
	private := "/private" + dir
	for _, c := range []struct{ name, registered, wd string }{
		{"registered under /var, run under /private/var", dir, private},
		{"registered under /private/var, run under /var", private, dir},
	} {
		t.Run(c.name, func(t *testing.T) {
			home := tempDir(t)
			repo := filepath.Join(c.registered, "project")
			slot := filepath.Join(repo, ".claude", "worktrees", "slot-1")
			if err := os.MkdirAll(slot, 0o755); err != nil {
				t.Fatal(err)
			}
			reg, err := registry.Add(t.Context(), home, repo)
			if err != nil {
				t.Fatal(err)
			}
			err = pool.Update(t.Context(), home, reg.Key, func(p *pool.Pool) error {
				p.Repo, p.Base = repo, "main"
				p.Slots = []pool.Slot{{Name: "slot-1", Path: slot, State: pool.StateReady}}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			wd := filepath.Join(c.wd, "project", ".claude", "worktrees", "slot-1")
			r := runStatus(t, home, wd)
			checkSucceeded(t, r)
			if want := repo + " (base main)\n"; !strings.HasPrefix(r.stdout, want) {
				t.Errorf("stdout = %q, want it to start %q", r.stdout, want)
			}
		})
	}
}
