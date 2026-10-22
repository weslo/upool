package clone

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/weslo/upool/internal/git"
	"github.com/weslo/upool/internal/paths"
	"github.com/weslo/upool/internal/pool"
	"github.com/weslo/upool/internal/proc"
)

// worktreesDir is where slots live, relative to the main checkout: <repo>/.claude/worktrees/slot-N
// (docs/contracts.md (e), the slot's path).
const worktreesDir = ".claude/worktrees"

// slotPrefix is the prefix of every slot's name, slot-N.
const slotPrefix = "slot-"

// excluded are the top-level entries step 4 does not copy, exactly: what Unity regenerates, the
// seed's own git directory, and, through copyAgentDir, .claude/worktrees (docs/contracts.md (g),
// the exclusion list).
var excluded = map[string]bool{".git": true, "Temp": true, "Logs": true, "obj": true,
	"Builds": true}

// agentDir is the top-level entry copied without its worktrees child, which holds the slot being
// made and every other slot.
const agentDir = ".claude"

// instanceFiles are the Editor's instance files step 5 strips, and pipelineDir the directory it
// strips with them. After a clean exit the instance files are already gone and Pipeline survives
// with one file; they are stripped anyway, since on Windows the instance files made `unity test`
// refuse (docs/design.md, "Slot lifecycle: seed only from a Library current at HEAD, refresh on
// handout, reset without `-x`"; spike §2 T1.5–T1.6).
var instanceFiles = []string{"Library/EditorInstance.json", "Library/ProtocolInstance.json"}

const pipelineDir = "Library/Pipeline"

// Recipe is what the clone recipe runs with.
type Recipe struct {
	// Git runs every git call, each with UNITY_SKIP_HOOKS set (internal/git).
	Git git.Client
	// Copier copies the seed's top-level entries (step 4); CP outside tests.
	Copier Copier
	// Proc reads OwnerPID's start time.
	Proc proc.Checker
	// OwnerPID is the cloning process, written with its start time into the creating entry so
	// that a clone that dies part-way is found by its dead owner ((f) transition 21; A4).
	OwnerPID int
	// Keys compares the slot's `unity cache key` across step 7's checkout (A20); its zero value,
	// with no unity on its PATH, compares nothing.
	Keys KeyCheck
	// Warn, when not nil, is told what the caller should warn about: a changed cache key, or one
	// that could not be compared.
	Warn func(string)
	// MaxSlots, when above 0, is the pool's max_slots: a clone that would make more slots than
	// that is refused, under the lock, with an error wrapping ErrPoolFull (docs/design.md, "Leases
	// that survive crashes and never reclaim a live tree": growth stops at max_slots).
	MaxSlots int
	// Refresher refreshes a stale seed in step 1; with none, a stale seed is refused.
	Refresher *SeedRefresher
}

// ErrPoolFull is the error Clone wraps when the pool already has MaxSlots slots.
var ErrPoolFull = errors.New("the pool is at max_slots")

// Clone runs docs/contracts.md (g) Clone recipe, steps 1 to 8, from seed, an absolute path to a
// checkout of the repository whose pool is ~/.upool/pools/<key>/state.json under home. It adds the
// next free slot-N to the pool as creating, makes it, and returns the slot as it is written once
// it is ready ((f) transitions 1 and 2).
//
// Choosing the seed is the caller's (step 0): init passes the main checkout once it has checked
// that its Editor is closed. A stale seed is refreshed by Refresher, and the check runs again:
// the clone goes on if the seed is current, and fails if it is still stale (#98).
//
// The lock is held only while state.json is written, never across the copy (docs/contracts.md
// (e)). A clone that fails after step 2 leaves its slot creating with this process as its owner,
// so once the process exits the slot is recovered by state, as a dead owner's is ((f)
// transition 21).
func (r Recipe) Clone(ctx context.Context, home, key, seed string) (pool.Slot, error) {
	// Step 1, which also gives the seed_head and seed_dirty that the slot records.
	s, err := CheckStaleness(ctx, r.Git, seed)
	if err != nil {
		return pool.Slot{}, err
	}
	if s.Stale() && r.Refresher == nil {
		return pool.Slot{}, fmt.Errorf("the Library in %s is stale: %s", seed, s.Reason())
	}
	if s.Stale() {
		warnings, err := r.Refresher.Refresh(ctx, seed)
		for _, w := range warnings {
			r.warn(w)
		}
		if err != nil {
			return pool.Slot{}, fmt.Errorf("refreshing the stale Library in %s: %w", seed, err)
		}
		if s, err = CheckStaleness(ctx, r.Git, seed); err != nil {
			return pool.Slot{}, err
		}
		if s.Stale() {
			return pool.Slot{}, fmt.Errorf("the Library in %s is still stale after its refresh: "+
				"%s", seed, s.Reason())
		}
	}
	slot, repo, base, err := r.reserve(ctx, home, key)
	if err != nil {
		return pool.Slot{}, err
	}
	pending, err := r.build(ctx, seed, s, repo, base, slot.Path)
	if err != nil {
		return pool.Slot{}, fmt.Errorf("cloning %s into %s: %w", slot.Name, slot.Path, err)
	}
	if pending {
		r.warn(ReimportWarning(slot.Name))
	}
	return r.finish(ctx, home, key, slot.Name, s, pending)
}

// reserve is step 2: under the lock, it adds the next free slot-N to the pool as creating, with
// this process as its owner, and returns the slot and the pool's repo and base.
func (r Recipe) reserve(ctx context.Context, home, key string) (slot pool.Slot, repo,
	base string, err error) {
	start, err := r.Proc.Start(r.OwnerPID)
	if err != nil {
		return pool.Slot{}, "", "", fmt.Errorf("reading the start time of process %d: %w",
			r.OwnerPID, err)
	}
	err = pool.Update(ctx, home, key, func(p *pool.Pool) error {
		if p.Repo == "" {
			return fmt.Errorf("~/.upool/pools/%s/state.json has no pool to clone a slot into", key)
		}
		if r.MaxSlots > 0 && len(p.Slots) >= r.MaxSlots {
			return fmt.Errorf("%w: it has %d slots, and max_slots is %d", ErrPoolFull,
				len(p.Slots), r.MaxSlots)
		}
		state, err := pool.Next(pool.Slot{}, pool.CloneStart{})
		if err != nil {
			return err
		}
		name := nextName(p.Slots)
		pid := r.OwnerPID
		slot = pool.Slot{
			Name:          name,
			Path:          filepath.Join(p.Repo, filepath.FromSlash(worktreesDir), name),
			State:         state,
			OwnerPID:      &pid,
			OwnerPIDStart: &start,
		}
		p.Slots = append(p.Slots, slot)
		repo, base = p.Repo, p.Base
		return nil
	})
	if err != nil {
		return pool.Slot{}, "", "", err
	}
	return slot, repo, base, nil
}

// nextName returns slot-N for the lowest N from 1 that no slot in slots has.
func nextName(slots []pool.Slot) string {
	taken := make(map[string]bool, len(slots))
	for _, s := range slots {
		taken[s.Name] = true
	}
	for n := 1; ; n++ {
		if name := slotPrefix + strconv.Itoa(n); !taken[name] {
			return name
		}
	}
}

// build is steps 3 to 8, with the lock dropped: it registers the worktree at slot, copies the seed
// into it, strips the Editor's instance files, rebuilds the index, moves the slot to base and
// checks the result.
func (r Recipe) build(ctx context.Context, seed string, s Staleness, repo, base,
	slot string) (reimportPending bool, err error) {
	if err := r.Git.WorktreeAdd(ctx, repo, slot, s.Head); err != nil { // 3
		return false, err
	}
	if err := r.copySeed(ctx, seed, slot); err != nil { // 4
		return false, err
	}
	if err := strip(slot); err != nil { // 5
		return false, err
	}
	if err := r.Git.Reset(ctx, slot); err != nil { // 6
		return false, err
	}
	pending, note, err := r.Keys.Around(ctx, slot, func() error { // 7, with A20's comparison
		return r.moveToBase(ctx, slot, base, s.Dirty)
	})
	if err != nil {
		return false, err
	}
	if note != "" {
		r.warn(note)
	}
	return pending, r.verify(ctx, repo, slot, base) // 8
}

// warn passes a warning to Warn, if the caller gave one.
func (r Recipe) warn(line string) {
	if r.Warn != nil {
		r.Warn(line)
	}
}

// copySeed is step 4: `cp -cR` of each top-level entry of seed into slot, except the excluded
// ones, and .claude without its worktrees child.
func (r Recipe) copySeed(ctx context.Context, seed, slot string) error {
	entries, err := os.ReadDir(seed)
	if err != nil {
		return fmt.Errorf("reading %s: %w", seed, err)
	}
	for _, e := range entries {
		name := e.Name()
		if excluded[name] {
			continue
		}
		src, dst := filepath.Join(seed, name), filepath.Join(slot, name)
		if name == agentDir && e.IsDir() {
			err = r.copyAgentDir(ctx, src, dst)
		} else {
			err = r.Copier.Copy(ctx, src, dst)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// copyAgentDir copies the directory .claude from src to dst without its worktrees child: it makes
// dst with src's permissions, copies every other entry, then gives dst src's mtime, as `cp -cR`
// would have left it. A .claude that is a link rather than a directory is copied whole.
func (r Recipe) copyAgentDir(ctx context.Context, src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("checking %s: %w", src, err)
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return fmt.Errorf("reading %s: %w", src, err)
	}
	if err := os.Mkdir(dst, info.Mode().Perm()); err != nil {
		return fmt.Errorf("making %s: %w", dst, err)
	}
	for _, e := range entries {
		if e.Name() == filepath.Base(worktreesDir) {
			continue
		}
		if err := r.Copier.Copy(ctx, filepath.Join(src, e.Name()),
			filepath.Join(dst, e.Name())); err != nil {
			return err
		}
	}
	if err := os.Chtimes(dst, info.ModTime(), info.ModTime()); err != nil {
		return fmt.Errorf("setting the mtime of %s: %w", dst, err)
	}
	return nil
}

// strip is step 5: `rm -f` of the instance files and `rm -rf` of Library/Pipeline in slot.
func strip(slot string) error {
	for _, name := range instanceFiles {
		file := filepath.Join(slot, filepath.FromSlash(name))
		if err := os.Remove(file); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("removing %s: %w", file, err)
		}
	}
	dir := filepath.Join(slot, filepath.FromSlash(pipelineDir))
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("removing %s: %w", dir, err)
	}
	return nil
}

// moveToBase is step 7 (A10): if the seed was dirty, revert what it left in slot, keeping
// gitignored files; then, if HEAD is not base's commit, check base out detached. A seed clean at
// base makes it do nothing.
func (r Recipe) moveToBase(ctx context.Context, slot, base string, dirty bool) error {
	if dirty {
		if err := r.Git.CheckoutFiles(ctx, slot); err != nil {
			return err
		}
		if err := r.Git.Clean(ctx, slot); err != nil {
			return err
		}
	}
	head, want, err := r.heads(ctx, slot, base)
	if err != nil || head == want {
		return err
	}
	return r.Git.CheckoutDetach(ctx, slot, base)
}

// heads returns the commit HEAD is at in slot and base's commit.
func (r Recipe) heads(ctx context.Context, slot, base string) (head, want string, err error) {
	if head, err = r.Git.Head(ctx, slot); err != nil {
		return "", "", err
	}
	if want, err = r.Git.Commit(ctx, slot, base); err != nil {
		return "", "", err
	}
	return head, want, nil
}

// verify is step 8: slot's status is empty, its HEAD is base, its .git file points back into
// <repo>/.git/worktrees/, and it is in `git worktree list`. Nothing trusts a slot that fails one
// (docs/design.md, "Leases that survive crashes and never reclaim a live tree").
func (r Recipe) verify(ctx context.Context, repo, slot, base string) error {
	status, err := r.Git.Status(ctx, slot)
	if err != nil {
		return err
	}
	if len(status) > 0 {
		return fmt.Errorf("git status lists %d changes after the clone, the first %q %s",
			len(status), status[0].XY, status[0].Path)
	}
	head, want, err := r.heads(ctx, slot, base)
	if err != nil {
		return err
	}
	if head != want {
		return fmt.Errorf("HEAD is %s, not base %s at %s", head, base, want)
	}
	if err := CheckBacklink(repo, slot); err != nil {
		return err
	}
	return r.checkListed(ctx, repo, slot)
}

// CheckBacklink checks that slot's .git is a file whose gitdir is inside <repo>/.git/worktrees/,
// compared by internal/paths keys. A relative gitdir is relative to slot, as git writes it under
// worktree.useRelativePaths. Step 8 runs it on a new slot, and doctor on every slot
// (docs/design.md, "Leases that survive crashes and never reclaim a live tree": a slot's backlink
// is authenticated before it is trusted).
func CheckBacklink(repo, slot string) error {
	file := filepath.Join(slot, ".git")
	info, err := os.Lstat(file)
	if err != nil {
		return fmt.Errorf("checking %s: %w", file, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a file, as a worktree's is", file)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return fmt.Errorf("reading %s: %w", file, err)
	}
	gitdir, ok := strings.CutPrefix(strings.TrimRight(string(data), "\r\n"), "gitdir: ")
	if !ok {
		return fmt.Errorf("%s has no gitdir line", file)
	}
	if !filepath.IsAbs(gitdir) {
		gitdir = filepath.Join(slot, gitdir)
	}
	worktrees := filepath.Join(repo, ".git", "worktrees")
	got, err := paths.Key(gitdir)
	if err != nil {
		return err
	}
	want, err := paths.Key(worktrees)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(got, want+string(filepath.Separator)) {
		return fmt.Errorf("%s points to %s, outside %s", file, gitdir, worktrees)
	}
	return nil
}

// checkListed checks that `git worktree list` in repo lists slot, compared by internal/paths keys.
func (r Recipe) checkListed(ctx context.Context, repo, slot string) error {
	want, err := paths.Key(slot)
	if err != nil {
		return err
	}
	wts, err := r.Git.WorktreeList(ctx, repo)
	if err != nil {
		return err
	}
	for _, wt := range wts {
		got, err := paths.Key(wt.Path)
		if err != nil {
			return err
		}
		if got == want {
			return nil
		}
	}
	return fmt.Errorf("git worktree list in %s does not list %s", repo, slot)
}

// finish marks the slot ready under the lock, with no owner, the seed's HEAD and dirtiness
// recorded, and reimport_pending set if step 7 changed its cache key, and returns it as written. The slot must still be the creating one this process
// reserved.
func (r Recipe) finish(ctx context.Context, home, key, name string, s Staleness,
	reimportPending bool) (pool.Slot, error) {
	var slot pool.Slot
	err := pool.Update(ctx, home, key, func(p *pool.Pool) error {
		for i := range p.Slots {
			sl := &p.Slots[i]
			if sl.Name != name {
				continue
			}
			if sl.OwnerPID == nil || *sl.OwnerPID != r.OwnerPID {
				return fmt.Errorf("%s is no longer this clone's: its owner is not process %d",
					name, r.OwnerPID)
			}
			state, err := pool.Next(*sl, pool.CloneFinish{})
			if err != nil {
				return err
			}
			sl.State, sl.OwnerPID, sl.OwnerPIDStart = state, nil, nil
			sl.SeedHead, sl.SeedDirty, sl.ReimportPending = s.Head, s.Dirty, reimportPending
			slot = *sl
			return nil
		}
		return fmt.Errorf("%s is no longer in ~/.upool/pools/%s/state.json", name, key)
	})
	return slot, err
}
