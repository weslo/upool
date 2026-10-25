package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/weslo/upool/internal/block"
	"github.com/weslo/upool/internal/config"
	"github.com/weslo/upool/internal/git"
	"github.com/weslo/upool/internal/ledger"
	"github.com/weslo/upool/internal/output"
	"github.com/weslo/upool/internal/paths"
	"github.com/weslo/upool/internal/pool"
	"github.com/weslo/upool/internal/registry"
)

// deinitUsage is deinit's usage line, which its handler also names when it refuses arguments.
const deinitUsage = "deinit [--yes] [--json]"

// deinitCommand is `upool deinit`, run from Terminal inside a pooled project's main checkout: it
// takes upool out of the project, the inverse of init (docs/design.md, "One binary, a dozen
// commands, one JSON file per pool", the `deinit` row; #191). Like prune it is a dry run unless
// --yes. It refuses, changing nothing, while a slot is in use or holds work; otherwise it removes
// the slots and the clean subagent worktrees with `git worktree remove`, without --force, the
// pool's state and registry entry, and what init installed in the project: upool's hook handlers,
// as init recognises them (A24; A26), and .upool.toml, with the skill and the instruction blocks an
// earlier upool's init installed, from files git does not track (A43). The .gitignore line, every
// branch and upool itself stay.
var deinitCommand = command{
	usage: deinitUsage,
	setup: func(fs *flag.FlagSet) handler {
		yes := fs.Bool("yes", false, "take upool out of the project, rather than only listing "+
			"what that removes")
		return func(inv *invocation) (any, []string, error) { return deinit(inv, *yes) }
	},
}

// deinitReport is what deinit found and did: the data of its --json envelope.
type deinitReport struct {
	// Repo is the main checkout, and Key its <repo-key>.
	Repo string `json:"repo"`
	Key  string `json:"key"`
	// DryRun is whether deinit only listed, without --yes.
	DryRun bool `json:"dry_run"`
	// Registered is whether ~/.upool/registry.json named the repository, and State the pool's
	// ~/.upool/pools/<key>/, or "" when there was none.
	Registered bool   `json:"registered"`
	State      string `json:"state,omitempty"`
	// Slots are the pool's slots, Agents the subagent worktrees deinit removes, and KeptAgents
	// those it leaves, with why: they stay ordinary git worktrees.
	Slots      []deinitSlot     `json:"slots"`
	Agents     []agentCandidate `json:"agents"`
	KeptAgents []keptAgent      `json:"kept_agents"`
	// Files are what deinit takes out of the project.
	Files []deinitFile `json:"files"`

	// stateRemoved is whether --yes removed State and the registry entry.
	stateRemoved bool
}

// deinitSlot is one of the pool's slots.
type deinitSlot struct {
	Name  string     `json:"name"`
	Path  string     `json:"path"`
	State pool.State `json:"state"`
	// Gone is whether its directory was already gone, so that only its entry goes, and Unlisted
	// whether git does not list it, so that its directory is left for a person to delete.
	Gone     bool `json:"gone"`
	Unlisted bool `json:"unlisted"`
	// Removed is whether --yes removed it, and Error why it could not.
	Removed bool   `json:"removed"`
	Error   string `json:"error,omitempty"`

	// slot is the entry as deinit read it, which it removes only if state.json still holds it.
	slot pool.Slot
}

// deinitFile is a file or directory under the project that deinit takes upool out of.
type deinitFile struct {
	Path string `json:"path"`
	// What is upool's part of it: "hooks", "block", "skill" or "repo_file".
	What string `json:"what"`
	// Removed is whether --yes took it out, and Note anything left behind.
	Removed bool   `json:"removed"`
	Note    string `json:"note,omitempty"`
}

// Human returns a line per thing deinit would remove, removed or could not remove, then what it
// keeps. A dry run ends saying how to go ahead.
func (r deinitReport) Human() string {
	if r.State == "" && !r.Registered && len(r.Files) == 0 {
		return r.Repo + " has nothing of upool's to remove"
	}
	verb := "removed"
	if r.DryRun {
		verb = "would remove"
	}
	lines := []string{r.Repo + ":"}
	// say adds the line for what, which deinit would remove, removed or could not remove.
	say := func(what, failed string, removed bool) {
		if failed == "" {
			failed = "not reached"
		}
		switch {
		case r.DryRun || removed:
			lines = append(lines, "  "+verb+" "+what)
		default:
			lines = append(lines, "  did not remove "+what+": "+failed)
		}
	}
	for _, s := range r.Slots {
		what := s.Name + ", " + s.Path
		switch {
		case s.Gone:
			what = s.Name + "'s entry: its directory is already gone"
		case s.Unlisted:
			what = s.Name + "'s entry, leaving " + s.Path + ", which git does not list, to " +
				"delete by hand"
		}
		say(what, s.Error, s.Removed)
	}
	for _, a := range r.Agents {
		say("agent worktree "+a.Path, a.Error, a.Removed)
	}
	if r.State != "" || r.Registered {
		say("upool's state, "+r.State+", and the repository's entry in ~/.upool/registry.json",
			"", r.stateRemoved)
	}
	for _, f := range r.Files {
		what := map[string]string{"hooks": "upool's hooks from ", "block": "upool's block from ",
			"skill": "the upool skill, ", "repo_file": ""}[f.What] + f.Path
		say(what, "", f.Removed)
		if f.Note != "" {
			lines = append(lines, "    "+f.Note)
		}
	}
	for _, k := range r.KeptAgents {
		lines = append(lines, fmt.Sprintf("  leaving agent worktree %s: %s", k.Path, k.Why))
	}
	lines = append(lines, "  keeping the "+worktreesLine+" line in .gitignore, every branch, "+
		"and upool itself")
	if r.DryRun {
		lines = append(lines, "this was a dry run: upool deinit --yes removes them")
	}
	return strings.Join(lines, "\n")
}

// deiniter takes upool out of one project.
type deiniter struct {
	inv *invocation
	g   git.Client
	// top is the main checkout as git names it, which deinit edits files in, and pr the pruner
	// whose checks and agent-worktree removal deinit shares.
	top string
	pr  pruner
}

// deinit is deinit's handler.
func deinit(inv *invocation, yes bool) (any, []string, error) {
	if len(inv.args) > 0 {
		return nil, nil, fmt.Errorf("unexpected arguments %q; usage: upool %s", inv.args,
			deinitUsage)
	}
	ctx := context.Background()
	wd, err := inv.getwd()
	if err != nil {
		return nil, nil, fmt.Errorf("reading the working directory: %w", err)
	}
	d := deiniter{inv: inv, g: git.Client{Runner: git.Exec{}, Env: inv.environ()}}
	if d.top, err = d.g.TopLevel(ctx, wd); err != nil {
		return nil, nil, fmt.Errorf("upool deinit runs inside a pooled project's git "+
			"repository: %w", err)
	}
	canonical, err := paths.Key(d.top)
	if err != nil {
		return nil, nil, err
	}
	if err := checkMainCheckout(inv.home, canonical); err != nil {
		return nil, nil, err
	}
	r := deinitReport{Repo: d.top, Key: registry.Key(canonical), DryRun: !yes,
		Slots: []deinitSlot{}, Agents: []agentCandidate{}, KeptAgents: []keptAgent{},
		Files: []deinitFile{}}
	if _, r.Registered, err = registry.Lookup(inv.home, d.top); err != nil {
		return nil, nil, err
	}
	p, err := pool.Load(inv.home, r.Key)
	if err != nil {
		return nil, nil, err
	}
	stateDir := filepath.Join(inv.home, ".upool", "pools", r.Key)
	if _, err := os.Lstat(stateDir); err == nil {
		r.State = stateDir
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, nil, fmt.Errorf("checking %s: %w", stateDir, err)
	}
	d.pr = pruner{inv: inv, g: d.g, rp: registeredPool{repo: registry.Repo{Path: canonical,
		Key: r.Key}, pool: p}}
	if len(p.Slots)+len(p.AgentWorktrees) > 0 {
		if err := d.plan(ctx, p, &r); err != nil {
			return nil, nil, err
		}
	}
	hooks, warnings, err := d.planFiles(ctx, &r)
	if err != nil {
		return nil, nil, err
	}
	if !yes {
		return r, warnings, nil
	}
	data, more, err := d.remove(ctx, &r, hooks)
	return data, append(warnings, more...), err
}

// checkMainCheckout refuses canonical, a repository's top level, when it is a slot or a worktree
// under a registered pool's .claude/worktrees: deinit runs in the pool's main checkout, and in a
// slot it would take upool's files out of the slot's copies instead.
func checkMainCheckout(home, canonical string) error {
	repos, err := registry.List(home)
	if err != nil {
		return err
	}
	for _, repo := range repos {
		dir := filepath.Join(repo.Path, filepath.FromSlash(strings.TrimSuffix(worktreesLine, "/")))
		if paths.Within(canonical, dir) {
			return output.Fail(output.CodeCommandFailed, "%s is a worktree of the pool %s: run "+
				"upool deinit in %s", canonical, repo.Path, repo.Path)
		}
	}
	return nil
}

// plan lists in r the pool's slots and its subagent worktrees, or refuses, naming every slot in
// the way, when deinit would take a slot that is in use or holds work: a leased slot whose holder
// is not shown dead, a slot an upool process is still cloning or warming, a parked slot whose
// directory is still there, a slot git has locked with no lease deinit ends, and a slot an Editor
// has open, that is dirty, define-order noise aside, or that holds another worktree; and while a
// detached `upool refresh` is topping the pool up. A subagent worktree that prune's checks would
// keep is left, and listed with why; one whose directory is gone, or that git does not list, goes
// with the pool's state.
func (d deiniter) plan(ctx context.Context, p pool.Pool, r *deinitReport) error {
	cfg, err := config.Load(config.Inputs{Getenv: d.inv.getenv, Repo: d.top, Home: d.inv.home})
	if err != nil {
		return err
	}
	editors, err := ledger.Load(d.inv.home)
	if err != nil {
		return err
	}
	listed, err := worktreeKeys(ctx, d.g, d.top)
	if err != nil {
		return err
	}
	wts, err := d.g.WorktreeList(ctx, d.top)
	if err != nil {
		return err
	}
	locked := map[string]bool{}
	for _, wt := range wts {
		key, err := paths.Key(wt.Path)
		if err != nil {
			return err
		}
		locked[key] = wt.Locked
	}
	var blocked []string
	if j := p.RefreshJob; j != nil {
		live, err := d.alive(j.PID, &j.PIDStart)
		if err != nil {
			return err
		}
		if live {
			blocked = append(blocked, fmt.Sprintf("upool refresh, process %d, is topping the "+
				"pool up: wait for it to end", j.PID))
		}
	}
	for _, s := range p.Slots {
		ds := deinitSlot{Name: s.Name, Path: s.Path, State: s.State, slot: s}
		key, err := paths.Key(s.Path)
		if err != nil {
			return err
		}
		info, err := os.Lstat(s.Path)
		ds.Gone = errors.Is(err, fs.ErrNotExist)
		if err != nil && !ds.Gone {
			return fmt.Errorf("checking %s: %w", s.Path, err)
		}
		ds.Unlisted = !ds.Gone && (!info.IsDir() || !listed[key])
		why, err := d.inUse(ctx, s, cfg, editors, listed, ds.Gone, ds.Unlisted, locked[key])
		if err != nil {
			return err
		}
		if why != "" {
			blocked = append(blocked, s.Name+" "+why)
		}
		r.Slots = append(r.Slots, ds)
	}
	if len(blocked) > 0 {
		return output.Fail(output.CodeCommandFailed, "not taking upool out of %s, which is "+
			"left as it was:\n  %s", d.top, strings.Join(blocked, "\n  "))
	}
	for _, a := range p.AgentWorktrees {
		key, err := paths.Key(a.Path)
		if err != nil {
			return err
		}
		if info, err := os.Stat(a.Path); err != nil || !info.IsDir() || !listed[key] {
			continue
		}
		why, err := d.pr.kept(ctx, editors, listed, a.Path)
		if err != nil {
			return err
		}
		if why != "" {
			r.KeptAgents = append(r.KeptAgents, keptAgent{Path: a.Path, Why: why})
			continue
		}
		r.Agents = append(r.Agents, agentCandidate{Path: a.Path, ParentPID: a.ParentPID,
			CreatedAt: a.CreatedAt, Why: "clean"})
	}
	return nil
}

// inUse returns why deinit must not take the slot s, after its name, and how to clear it, or "".
// gone says whether its directory is gone, unlisted whether git does not list it, and locked
// whether git has it locked.
func (d deiniter) inUse(ctx context.Context, s pool.Slot, cfg config.Config,
	editors ledger.Ledger, listed map[string]bool, gone, unlisted, locked bool) (string, error) {
	if s.OwnerPID != nil {
		live, err := d.alive(*s.OwnerPID, s.OwnerPIDStart)
		if err != nil || live {
			return fmt.Sprintf("is %s, by upool process %d: wait for it to finish", s.State,
				*s.OwnerPID), err
		}
	}
	if l := s.Lease; l != nil {
		holder := l.Holder
		if holder == "" {
			holder = "a session"
		}
		switch {
		case !l.Confirmed:
			timeout := cfg.ConfirmTimeout.Value
			if !s.ConfirmExpired(d.inv.now(), timeout) {
				return fmt.Sprintf("has an unconfirmed lease from %s, which a session starting "+
					"in it may yet confirm: wait up to %s for it to be confirmed or to expire, "+
					"then run upool deinit again", holder, leaseAge(timeout)), nil
			}
		case l.HolderPID == nil:
			return fmt.Sprintf("is leased by an upool take with no --holder: once that work is "+
				"done, run upool release %s", s.Path), nil
		default:
			live, err := d.alive(*l.HolderPID, l.HolderPIDStart)
			if err != nil || live {
				return fmt.Sprintf("is leased to %s, process %d, which upool cannot show has "+
					"ended: archive the session with the desktop app's Archive button, or end "+
					"the process, then run upool deinit again", holder, *l.HolderPID), err
			}
		}
	}
	if gone {
		return "", nil
	}
	if s.State == pool.StateParked && unlisted {
		return fmt.Sprintf("is parked, holding work, in a directory git does not list: save "+
			"what you need from %s, delete it, then run upool deinit again", s.Path), nil
	}
	if s.State == pool.StateParked {
		return fmt.Sprintf("is parked, holding work: save what you need from it, remove it "+
			"with git worktree remove --force %s, then run upool deinit again", s.Path), nil
	}
	if unlisted {
		return "", nil
	}
	if locked && s.Lease == nil {
		return fmt.Sprintf("is locked in git: once nothing needs the lock, run git worktree "+
			"unlock %s, then upool deinit again", s.Path), nil
	}
	why, err := d.pr.kept(ctx, editors, listed, s.Path)
	if err != nil || why == "" {
		return "", err
	}
	return "stays, since " + why + ": deal with that, then run upool deinit again", nil
}

// alive reports whether the process pid, started at start, is running. A process whose start
// time was not recorded, or whose life cannot be read here, counts as running: deinit never takes
// a slot from a process it cannot show dead.
func (d deiniter) alive(pid int, start *time.Time) (bool, error) {
	if start == nil {
		return true, nil
	}
	live, err := d.inv.checker().Alive(pid, *start)
	if errors.Is(err, errors.ErrUnsupported) {
		return true, nil
	}
	return live, err
}

// planFiles lists in r what init installed in the project that is still there, and returns the
// change to .claude/settings.local.json, and warnings. An instruction file whose markers do not
// pair is refused before anything is changed. A block or a skill an earlier upool wrote into a file
// git tracks is left, with a warning naming it, since deinit never writes into a tracked file, and
// a SKILL.md is upool's only when its front matter names the upool skill (A43; #230).
func (d deiniter) planFiles(ctx context.Context, r *deinitReport) (registration, []string,
	error) {
	var warnings []string
	hooks, err := planDeregistration(filepath.Join(d.top, projectSettings))
	if err != nil {
		return registration{}, nil, err
	}
	if hooks.changed {
		r.Files = append(r.Files, deinitFile{Path: hooks.file, What: "hooks"})
	}
	for _, name := range []string{claudeMD, agentsMD} {
		file := filepath.Join(d.top, name)
		doc, err := os.ReadFile(file)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return registration{}, nil, fmt.Errorf("reading %s: %w", file, err)
		}
		_, found, err := block.Strip(doc)
		if err != nil {
			return registration{}, nil, fmt.Errorf("removing upool's block from %s: %w", file,
				err)
		}
		if !found {
			continue
		}
		isTracked, err := tracked(ctx, d.g, d.top, name)
		if err != nil {
			return registration{}, nil, err
		}
		if isTracked {
			warnings = append(warnings, trackedWarning(file, "the block"))
			continue
		}
		r.Files = append(r.Files, deinitFile{Path: file, What: "block"})
	}
	var seen []string
	for _, name := range skillFiles {
		dir := filepath.Dir(filepath.Join(d.top, filepath.FromSlash(name)))
		ours, err := upoolSkill(filepath.Join(dir, "SKILL.md"))
		if err != nil {
			return registration{}, nil, err
		}
		if !ours {
			continue
		}
		isTracked, err := tracked(ctx, d.g, d.top, name)
		if err != nil {
			return registration{}, nil, err
		}
		if isTracked {
			warnings = append(warnings, trackedWarning(filepath.Join(dir, "SKILL.md"),
				"the upool skill"))
			continue
		}
		// .claude/skills is often a link to .agents/skills, and then both copies are one.
		real, err := filepath.EvalSymlinks(dir)
		if err != nil {
			return registration{}, nil, fmt.Errorf("resolving %s: %w", dir, err)
		}
		if slices.Contains(seen, real) {
			continue
		}
		seen = append(seen, real)
		r.Files = append(r.Files, deinitFile{Path: dir + string(filepath.Separator),
			What: "skill"})
	}
	repoFile := config.RepoPath(d.top)
	if _, err := os.Lstat(repoFile); err == nil {
		r.Files = append(r.Files, deinitFile{Path: repoFile, What: "repo_file"})
	} else if !errors.Is(err, fs.ErrNotExist) {
		return registration{}, nil, fmt.Errorf("checking %s: %w", repoFile, err)
	}
	return hooks, warnings, nil
}

// remove takes upool out of the project, as r lists it. Each slot goes first, as removeSlot
// says; one that cannot stops deinit with its entry kept, and the registry entry, the pool's state
// and the project's files untouched, so that the pool still works and a later deinit finishes the
// job. Then the clean subagent worktrees go, as prune removes them, then the registry entry and
// the pool's state, the entry put back if the state cannot go, and last init's files.
func (d deiniter) remove(ctx context.Context, r *deinitReport, hooks registration) (any,
	[]string, error) {
	for i := range r.Slots {
		if err := d.removeSlot(ctx, r.Key, &r.Slots[i]); err != nil {
			r.Slots[i].Error = err.Error()
			return r, nil, output.Fail(output.CodeCommandFailed, "taking upool out of %s "+
				"stopped at %s: %v; run upool deinit --yes again once that is dealt with", d.top,
				r.Slots[i].Name, err)
		}
	}
	for i := range r.Agents {
		d.pr.removeAgent(ctx, &r.Agents[i])
	}
	if r.Registered {
		if _, err := registry.Remove(ctx, d.inv.home, d.top); err != nil {
			return r, nil, err
		}
	}
	err := pool.Delete(ctx, d.inv.home, r.Key, func(p pool.Pool) error {
		if len(p.Slots) > 0 {
			return fmt.Errorf("the pool has slots again: %s", p.Slots[0].Name)
		}
		return nil
	})
	if err != nil {
		if r.Registered {
			if _, aerr := registry.Add(ctx, d.inv.home, d.top); aerr != nil {
				err = fmt.Errorf("%w; putting its registry entry back: %v", err, aerr)
			}
		}
		return r, nil, fmt.Errorf("removing %s: %w", r.State, err)
	}
	r.stateRemoved = true
	for i := range r.Files {
		if err := d.removeFile(&r.Files[i], hooks); err != nil {
			return r, nil, err
		}
	}
	return r, nil, nil
}

// removeSlot removes the slot s as deinit read it. Under the lock it takes the entry out of
// state.json, if it is still as read, so that nothing hands the slot out meanwhile. Unless its
// directory is gone or git does not list it, it then unlocks a slot whose lease it ends, reverts
// define-order noise, asks prune's kept again, and runs `git worktree remove` without --force,
// which refuses a worktree with changes. If
// that fails while git still lists the slot, the entry goes back; if git has let go of it, the
// entry stays out, and s names what is left to delete.
func (d deiniter) removeSlot(ctx context.Context, key string, s *deinitSlot) error {
	home := d.inv.home
	err := pool.Update(ctx, home, key, func(p *pool.Pool) error {
		i := slices.IndexFunc(p.Slots, func(x pool.Slot) bool { return x.Name == s.Name })
		if i < 0 {
			return fmt.Errorf("it left state.json meanwhile")
		}
		if now := p.Slots[i]; now.Path != s.slot.Path || now.State != s.slot.State ||
			leaseID(now) != leaseID(s.slot) {
			return fmt.Errorf("it changed in state.json meanwhile: it is %s", now.State)
		}
		p.Slots = slices.Delete(p.Slots, i, i+1)
		return nil
	})
	if err != nil {
		return err
	}
	// A lease deinit ends unlocks the slot, if an earlier upool locked it, as every end of a
	// lease does (A27); git prunes no locked worktree, even one whose directory is gone.
	if s.slot.Lease != nil {
		err = pool.Unlock(ctx, d.g, d.top, s.Path)
	}
	if err == nil && s.Gone {
		// git keeps a worktree whose directory went without it until it is pruned, and pruned
		// now, so that a run that stops at a later slot leaves no entry-less registration.
		err = d.g.WorktreePrune(ctx, d.top)
	}
	if err == nil && (s.Gone || s.Unlisted) {
		s.Removed = true
		return nil
	}
	if err == nil {
		err = d.pr.removeTaken(ctx, s.slot)
	}
	if err != nil {
		listed, lerr := worktreeKeys(ctx, d.g, d.top)
		slotKey, kerr := paths.Key(s.Path)
		if lerr == nil && kerr == nil && !listed[slotKey] {
			s.Removed, s.Error = true, err.Error()+"; git no longer lists it: delete what is "+
				"left of "+s.Path+" by hand"
			return nil
		}
		if perr := pool.Update(ctx, home, key, func(p *pool.Pool) error {
			p.Slots = append(p.Slots, s.slot)
			return nil
		}); perr != nil {
			return fmt.Errorf("%w; putting its entry back: %v", err, perr)
		}
		return err
	}
	s.Removed = true
	return nil
}

// leaseID is the ID of s's lease, or "" when it has none.
func leaseID(s pool.Slot) string {
	if s.Lease == nil {
		return ""
	}
	return s.Lease.LeaseID
}

// removeFile takes upool's part out of f: the hooks, as hooks plans them; the block, deleting a
// file left empty; the skill, its SKILL.md and then its directory, which stays if anything else is
// in it; or .upool.toml.
func (d deiniter) removeFile(f *deinitFile, hooks registration) error {
	switch f.What {
	case "hooks":
		if _, err := hooks.apply(); err != nil {
			return err
		}
	case "block":
		if _, err := block.Remove(f.Path); err != nil {
			return err
		}
	case "skill":
		dir := strings.TrimSuffix(f.Path, string(filepath.Separator))
		if err := os.Remove(filepath.Join(dir, "SKILL.md")); err != nil &&
			!errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("removing %s: %w", filepath.Join(dir, "SKILL.md"), err)
		}
		if err := removeSkillDir(dir); err != nil {
			return err
		}
		if _, err := os.Stat(dir); err == nil {
			f.Note = "kept " + dir + ": something other than upool's SKILL.md is in it"
		}
	case "repo_file":
		if err := os.Remove(f.Path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("removing %s: %w", f.Path, err)
		}
	}
	f.Removed = true
	return nil
}
