package main

import (
	"context"
	"flag"
	"fmt"

	"github.com/weslo/upool/internal/git"
	"github.com/weslo/upool/internal/output"
	"github.com/weslo/upool/internal/paths"
	"github.com/weslo/upool/internal/registry"
)

// pathUsage is path's usage line, which its handler also names when it refuses its arguments.
const pathUsage = "path <name> [--json]"

// pathCommand is `upool path <name>`: it prints the path of the slot that has the branch <name>
// checked out, and has no side effects (docs/design.md, "One binary, a dozen commands, one JSON
// file per pool", the `which`/`path` row). A lease has no name; `take <name>` names a branch, and
// path finds the slot whose HEAD is that branch in `git worktree list` (A18). Its --json data is
// which's: the slot's name, path, state and lease, and the pool's repo.
var pathCommand = command{
	usage: pathUsage,
	setup: func(*flag.FlagSet) handler { return pathOf },
}

// pathOf is path's handler. It reads the pool containing the working directory, as status does,
// and `git worktree list` in its main checkout, and compares canonical keys, so that a slot
// recorded under one spelling of a directory is found under another (docs/design.md, "Repository,
// CI and Homebrew distribution", the first of the five rules after the layout). With no slot on
// the branch, the main checkout included, it exits 1 with NO_SUCH_SLOT.
func pathOf(inv *invocation) (any, []string, error) {
	if len(inv.args) != 1 || inv.args[0] == "" {
		return nil, nil, fmt.Errorf("path needs one branch name; usage: upool %s", pathUsage)
	}
	name := inv.args[0]
	ctx := context.Background()
	wd, err := inv.getwd()
	if err != nil {
		return nil, nil, fmt.Errorf("reading the working directory: %w", err)
	}
	cwd, err := paths.Key(wd)
	if err != nil {
		return nil, nil, err
	}
	pools, err := readPools(ctx, inv.home,
		func(repo registry.Repo) bool { return paths.Within(cwd, repo.Path) })
	if err != nil {
		return nil, nil, err
	}
	if len(pools) == 0 {
		return nil, nil, output.Fail(output.CodeCommandFailed,
			"%s is not inside a registered pool; run upool path in a pool's checkout", wd)
	}
	rp := innermost(pools)
	repo := rp.pool.Repo
	if repo == "" {
		repo = rp.repo.Path
	}
	wts, err := git.Client{Runner: git.Exec{}, Env: inv.environ()}.WorktreeList(ctx, repo)
	if err != nil {
		return nil, nil, err
	}
	for _, wt := range wts {
		if wt.Branch != "refs/heads/"+name {
			continue
		}
		key, err := paths.Key(wt.Path)
		if err != nil {
			return nil, nil, err
		}
		for _, s := range rp.pool.Slots {
			sk, err := paths.Key(s.Path)
			if err != nil {
				return nil, nil, err
			}
			if sk == key {
				return *newWhichReport(s, repo), nil, nil
			}
		}
		return nil, nil, output.Fail(output.CodeNoSuchSlot, "no slot of %s has branch %s "+
			"checked out; %s has it, and is not a slot", repo, name, wt.Path)
	}
	return nil, nil, output.Fail(output.CodeNoSuchSlot,
		"no slot of %s has branch %s checked out", repo, name)
}
