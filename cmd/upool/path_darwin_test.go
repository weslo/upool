package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/weslo/upool/internal/output"
	"github.com/weslo/upool/internal/pool"
	"github.com/weslo/upool/internal/registry"
)

// TestPathMatchesThePrivateVarSpellingOfASlot makes a repository under /var/tmp and its slot with
// git under one spelling of /var, records the pool under the other, and runs path in each:
// `git worktree list` and state.json name the slot differently, and path still finds it, printing
// state.json's spelling. /var/tmp is used rather than t.TempDir, whose spelling depends on TMPDIR.
func TestPathMatchesThePrivateVarSpellingOfASlot(t *testing.T) {
	dir, err := os.MkdirTemp("/var/tmp", "upool-path-")
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
	for i, c := range []struct{ name, git, recorded string }{
		{"git under /var, recorded under /private/var", dir, private},
		{"git under /private/var, recorded under /var", private, dir},
	} {
		t.Run(c.name, func(t *testing.T) {
			home := tempDir(t)
			env := map[string]string{"HOME": home, "PATH": os.Getenv("PATH"),
				"GIT_CONFIG_NOSYSTEM": "1", "GIT_AUTHOR_NAME": "upool test",
				"GIT_AUTHOR_EMAIL": "test@upool.invalid", "GIT_COMMITTER_NAME": "upool test",
				"GIT_COMMITTER_EMAIL": "test@upool.invalid"}
			gitRepo := filepath.Join(c.git, "project-"+strconv.Itoa(i))
			git := func(args ...string) {
				t.Helper()
				cmd := exec.Command("git", args...)
				for k, v := range env {
					cmd.Env = append(cmd.Env, k+"="+v)
				}
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("git %v: %v: %s", args, err, out)
				}
			}
			git("init", "-q", "-b", "main", gitRepo)
			git("-C", gitRepo, "commit", "-q", "--allow-empty", "-m", "first")
			git("-C", gitRepo, "worktree", "add", "-q", "-b", "feature-x",
				filepath.Join(gitRepo, ".claude", "worktrees", "slot-1"))

			repo := filepath.Join(c.recorded, filepath.Base(gitRepo))
			slot := filepath.Join(repo, ".claude", "worktrees", "slot-1")
			reg, err := registry.Add(t.Context(), home, repo)
			if err != nil {
				t.Fatal(err)
			}
			err = pool.Update(t.Context(), home, reg.Key, func(p *pool.Pool) error {
				p.Repo, p.Base = repo, "main"
				p.Slots = []pool.Slot{{Name: "slot-1", Path: slot, State: pool.StateLeased}}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			for _, wd := range []string{gitRepo, repo} {
				env["PWD"] = wd
				r := dispatch(t, commandTable, hookTable, env, "", "path", "feature-x")
				if r.code != output.ExitOK || r.stdout != slot+"\n" {
					t.Errorf("from %s: exit %d, stdout %q, stderr %q; want 0 and %s", wd, r.code,
						r.stdout, r.stderr, slot)
				}
			}
		})
	}
}
