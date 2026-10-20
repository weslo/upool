//go:build darwin

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWhichMatchesThePrivateVarSpellingOfASlot registers a pool under one spelling of /var and runs
// which under the other. macOS spells the directory both ways, so a slot recorded as one is still
// the slot containing a working directory spelled as the other (docs/design.md, "Repository, CI
// and Homebrew distribution", the first of the five rules after the layout). /var/tmp is used
// rather than t.TempDir, whose spelling depends on TMPDIR.
func TestWhichMatchesThePrivateVarSpellingOfASlot(t *testing.T) {
	dir, err := os.MkdirTemp("/var/tmp", "upool-which-")
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
			w := newWhichPool(t, filepath.Join(c.registered, "project"))
			other := whichPool{home: w.home, repo: filepath.Join(c.wd, "project")}
			wd := filepath.Join(other.path("slot-1"), "Assets", "Scripts")
			checkFound(t, w.which(t, wd), w.path("slot-1"))
			checkNotInSlot(t, w.which(t, other.repo), other.repo)
		})
	}
}
