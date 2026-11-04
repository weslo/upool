//go:build darwin

package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/weslo/upool/internal/pool"
	"github.com/weslo/upool/internal/registry"
)

// With its refresh job running, status announces it without --all, from the main checkout and
// from inside a slot, where a session runs it, as --all does from the same directory (#283). Only
// macOS reads the process start time that tells a live job from a gone one (internal/proc).
func TestStatusShowsTheRunningRefreshWithoutAll(t *testing.T) {
	h := newStatusHome(t)
	repo, ok, err := registry.Lookup(h.home, h.down)
	if err != nil || !ok {
		t.Fatalf("looking up %s: found %t, error %v; want it registered", h.down, ok, err)
	}
	job := liveJob(t)
	err = pool.Update(t.Context(), h.home, repo.Key, func(p *pool.Pool) error {
		p.RefreshJob = job
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	line := "refreshing: upool refresh is running as process " + strconv.Itoa(job.PID)
	for _, wd := range []string{h.down, filepath.Join(h.down, ".claude", "worktrees", "slot-1")} {
		r := runStatus(t, h.home, wd)
		checkSucceeded(t, r)
		if !strings.Contains(r.stdout, line) {
			t.Errorf("from %s: stdout = %q, want the line %q", wd, r.stdout, line)
		}
		r = runStatus(t, h.home, wd, "--json")
		checkSucceeded(t, r)
		if !strings.Contains(r.stdout, `"pid": `+strconv.Itoa(job.PID)) {
			t.Errorf("from %s: --json = %s, want refresh_job naming process %d", wd, r.stdout,
				job.PID)
		}
	}
	checkShowsItsPoolAsAllDoes(t, h)
}

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
