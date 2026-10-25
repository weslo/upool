// Package git is the only way upool runs git (docs/design.md, "Repository, CI and Homebrew
// distribution", the internal/git row of the layout): every call goes through a Runner, and the
// default one, Exec, runs `git -C <dir> …` with UNITY_SKIP_HOOKS=1.
//
// The directory is always given with -C and must be absolute, so no call depends on the process's
// working directory: a hook runs wherever the desktop app's helper starts it, and a slot is never
// the directory upool was started in. UNITY_SKIP_HOOKS is set on every call so that Unity's managed
// git hooks cannot hang a checkout or a reset (docs/design.md, "Slot lifecycle: seed only from a
// Library current at HEAD, refresh on handout, reset without `-x`").
//
// Status, WorktreeList and HeadReachable are the reads the slot lifecycle is built on: a slot is
// clean when Status reports nothing and HeadReachable is true (docs/contracts.md (f) Slot
// lifecycle, the definition of clean; A5), and a slot that left WorktreeList is missing ((f)
// transition 17).
//
// The environment is always a parameter; nothing here reads the process's own (CONTRIBUTING.md,
// "Process state comes in through run").
package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// skipHooks is added to every git call's environment by Exec, after the caller's, so a caller's own
// UNITY_SKIP_HOOKS cannot turn it off.
const skipHooks = "UNITY_SKIP_HOOKS=1"

// Result is what one git command returned.
type Result struct {
	Stdout []byte
	Stderr []byte
	// ExitCode is git's exit status: 0 for success.
	ExitCode int
}

// Runner runs one git command, `git -C <dir> <args…>`, with env as its whole environment
// ("KEY=value" entries). A command that ran and failed is a Result with a non-zero ExitCode and a
// nil error; the error is for a command that could not be run or finish, such as git missing from
// PATH, a relative dir, or ctx ending. Unit tests replace it with recorded output.
type Runner interface {
	Run(ctx context.Context, dir string, env []string, args ...string) (Result, error)
}

// Exec is the Runner that runs the git binary found on the process's PATH.
type Exec struct{}

var _ Runner = Exec{}

// Run runs `git -C dir args…` with env plus UNITY_SKIP_HOOKS=1 as its environment. dir must be
// absolute: -C resolves a relative one against the process's working directory.
func (Exec) Run(ctx context.Context, dir string, env []string, args ...string) (Result, error) {
	if !filepath.IsAbs(dir) {
		return Result{}, fmt.Errorf("running git in %q: directory is not absolute", dir)
	}
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	// exec keeps the last of duplicate keys, so skipHooks wins over a caller's UNITY_SKIP_HOOKS.
	cmd.Env = append(slices.Clip(env), skipHooks)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	res := Result{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	var exit *exec.ExitError
	if errors.As(err, &exit) && ctx.Err() == nil {
		res.ExitCode = exit.ExitCode()
		return res, nil
	}
	if err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return res, fmt.Errorf("running %s: %w", command(dir, args), err)
	}
	return res, nil
}

// Client runs upool's git calls through Runner, each with Env as git's whole environment.
type Client struct {
	Runner Runner
	Env    []string
}

// run runs one command and returns its stdout, or an error carrying git's stderr when it exits
// non-zero.
func (c Client) run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	res, err := c.Runner.Run(ctx, dir, c.Env, args...)
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, exitError(dir, args, res)
	}
	return res.Stdout, nil
}

// command is how errors name a git call.
func command(dir string, args []string) string {
	return strings.Join(append([]string{"git", "-C", dir}, args...), " ")
}

// StatusEntry is one entry of `git status --porcelain=v1`.
type StatusEntry struct {
	// XY is the two status letters: index then worktree, "??" for an untracked path.
	XY string
	// Path is the path relative to the worktree's top level; an untracked directory ends in "/".
	Path string
	// OrigPath is the path a rename or copy came from, and empty for every other entry.
	OrigPath string
}

// statusArgs is the status call. --untracked-files=normal is git's default, given explicitly so
// that a user's status.showUntrackedFiles=no cannot hide the untracked file that parks a slot
// (testdata/captures/git/remove-status-untracked.txt). Ignored files are never listed, so a
// gitignored Library/ does not make a slot dirty.
var statusArgs = []string{"status", "--porcelain=v1", "-z", "--untracked-files=normal"}

// Status returns the worktree at dir's `git status --porcelain=v1 -z` entries, in git's order. No
// entries is the first half of docs/contracts.md (f)'s definition of clean; HeadReachable is the
// second.
func (c Client) Status(ctx context.Context, dir string) ([]StatusEntry, error) {
	out, err := c.run(ctx, dir, statusArgs...)
	if err != nil {
		return nil, err
	}
	entries, err := parseStatus(out)
	if err != nil {
		return nil, fmt.Errorf("parsing %s: %w", command(dir, statusArgs), err)
	}
	return entries, nil
}

// parseStatus parses porcelain v1 with -z: each entry is "XY PATH" and a NUL, and a rename or copy
// is followed by its original path and another NUL. Paths are never quoted under -z.
func parseStatus(out []byte) ([]StatusEntry, error) {
	fields, err := nulFields(out)
	if err != nil {
		return nil, err
	}
	var entries []StatusEntry
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		if len(f) < 4 || f[2] != ' ' {
			return nil, fmt.Errorf("malformed entry %q", f)
		}
		e := StatusEntry{XY: f[:2], Path: f[3:]}
		if strings.ContainsAny(e.XY, "RC") {
			i++
			if i == len(fields) {
				return nil, fmt.Errorf("entry %q has no original path", f)
			}
			e.OrigPath = fields[i]
		}
		entries = append(entries, e)
	}
	return entries, nil
}

// Worktree is one record of `git worktree list --porcelain`.
type Worktree struct {
	// Path is the worktree's top level as git prints it, before internal/paths makes it canonical.
	Path string
	// HEAD is the object name HEAD points at; empty for a bare repository.
	HEAD string
	// Branch is the full ref checked out, such as "refs/heads/main"; empty when Detached or Bare.
	Branch   string
	Detached bool
	Bare     bool
	Locked   bool
	// LockReason is the reason given to `git worktree lock --reason`, empty when there was none.
	LockReason string
	// Prunable reports that git would prune the worktree, such as when its directory is gone.
	Prunable bool
	// PrunableReason is git's reason, such as "gitdir file points to non-existent location".
	PrunableReason string
}

// worktreeListArgs is the list call. -z makes a path or lock reason with a newline in it parse,
// and leaves the lock reason unquoted.
var worktreeListArgs = []string{"worktree", "list", "--porcelain", "-z"}

// WorktreeList returns every worktree of the repository at repo, the main one first, from
// `git worktree list --porcelain -z`. repo can be any of its worktrees.
func (c Client) WorktreeList(ctx context.Context, repo string) ([]Worktree, error) {
	out, err := c.run(ctx, repo, worktreeListArgs...)
	if err != nil {
		return nil, err
	}
	wts, err := parseWorktreeList(out)
	if err != nil {
		return nil, fmt.Errorf("parsing %s: %w", command(repo, worktreeListArgs), err)
	}
	return wts, nil
}

// parseWorktreeList parses the porcelain format with -z: one "label value" or bare "label"
// attribute per NUL-terminated line, each record opened by "worktree <path>" and closed by an empty
// line. git says new attributes may be added, so a label this does not know is skipped.
func parseWorktreeList(out []byte) ([]Worktree, error) {
	lines, err := nulFields(out)
	if err != nil {
		return nil, err
	}
	var wts []Worktree
	open := false
	for _, line := range lines {
		if line == "" {
			if !open {
				return nil, errors.New("empty record")
			}
			open = false
			continue
		}
		label, value, _ := strings.Cut(line, " ")
		if label == "worktree" {
			if open {
				return nil, fmt.Errorf("record for %q is not terminated", wts[len(wts)-1].Path)
			}
			wts = append(wts, Worktree{Path: value})
			open = true
			continue
		}
		if !open {
			return nil, fmt.Errorf("attribute %q outside a record", line)
		}
		wt := &wts[len(wts)-1]
		switch label {
		case "HEAD":
			wt.HEAD = value
		case "branch":
			wt.Branch = value
		case "detached":
			wt.Detached = true
		case "bare":
			wt.Bare = true
		case "locked":
			wt.Locked, wt.LockReason = true, value
		case "prunable":
			wt.Prunable, wt.PrunableReason = true, value
		}
	}
	if open {
		return nil, fmt.Errorf("record for %q is not terminated", wts[len(wts)-1].Path)
	}
	return wts, nil
}

// headReachableArgs is the reachability call: every ref whose history contains HEAD.
var headReachableArgs = []string{"for-each-ref", "--contains", "HEAD", "--format=%(refname)"}

// HeadReachable reports whether some ref contains the HEAD of the worktree at dir:
// `git for-each-ref --contains HEAD` prints at least one line. It is false for a commit made on a
// detached HEAD, which a reset to base would orphan, so such a slot is dirty however clean Status
// says it is (docs/contracts.md (f), the definition of clean; A5).
func (c Client) HeadReachable(ctx context.Context, dir string) (bool, error) {
	out, err := c.run(ctx, dir, headReachableArgs...)
	if err != nil {
		return false, err
	}
	return len(bytes.TrimSpace(out)) > 0, nil
}

// topLevelArgs is the call that finds a worktree's top level.
var topLevelArgs = []string{"rev-parse", "--show-toplevel"}

// TopLevel returns the top level of the worktree containing dir, from
// `git rev-parse --show-toplevel`, with symlinks resolved as git resolves them. `upool init` takes
// it as the repository to register (docs/design.md, "One binary, a dozen commands, one JSON file
// per pool", the `init` row). Outside a repository it is an error carrying git's message.
func (c Client) TopLevel(ctx context.Context, dir string) (string, error) {
	out, err := c.run(ctx, dir, topLevelArgs...)
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(string(out), "\n"), nil
}

// headArgs is the call that names HEAD's commit, as docs/contracts.md (g) step 1 gives it.
var headArgs = []string{"rev-parse", "HEAD"}

// Head returns the commit HEAD points at in the worktree at dir, from `git rev-parse HEAD`. The
// staleness check records a seed's, which becomes the slot's seed_head (docs/contracts.md (g)
// Clone recipe, step 1; (e) State schemas). In a repository with no commits it is an error.
func (c Client) Head(ctx context.Context, dir string) (string, error) {
	out, err := c.run(ctx, dir, headArgs...)
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(string(out), "\n"), nil
}

// LsFiles returns the tracked paths under dir that match pathspecs, relative to dir and in git's
// order, from `git ls-files -z -- <pathspecs…>`; no pathspecs lists every tracked path. `init`
// refuses a repository that tracks anything under Library/, Unity's own check (docs/design.md,
// "Slot lifecycle: seed only from a Library current at HEAD, refresh on handout, reset without
// `-x`").
func (c Client) LsFiles(ctx context.Context, dir string, pathspecs ...string) ([]string, error) {
	args := append([]string{"ls-files", "-z", "--"}, pathspecs...)
	out, err := c.run(ctx, dir, args...)
	if err != nil {
		return nil, err
	}
	files, err := nulFields(out)
	if err != nil {
		return nil, fmt.Errorf("parsing %s: %w", command(dir, args), err)
	}
	return files, nil
}

// CheckIgnore reports whether git ignores path, relative to dir, from
// `git check-ignore -q -- <path>`: exit 0 when it does, 1 when it does not. The path need not
// exist, and a path inside an ignored directory is ignored, which is how `init` and `doctor` find
// that .claude/worktrees is ignored (docs/contracts.md (g) Clone recipe, the preconditions).
func (c Client) CheckIgnore(ctx context.Context, dir, path string) (bool, error) {
	_, ignored, err := c.runStatus(ctx, dir, "check-ignore", "-q", "--", path)
	return ignored, err
}

// LsExcludedBy returns the untracked paths under dir that the patterns in file, in .gitignore
// syntax, exclude, relative to dir and in git's order, from
// `git ls-files -z --others --ignored --exclude-from=<file>`: file's patterns are the only ones
// that count, not the repository's own. A directory listed, which ends in /, is a nested
// repository, such as a slot under .claude/worktrees, which git does not look inside. The mirror
// of a project's .worktreeinclude reads its candidates this way (internal/mirror; #233).
func (c Client) LsExcludedBy(ctx context.Context, dir, file string) ([]string, error) {
	args := []string{"ls-files", "-z", "--others", "--ignored", "--exclude-from=" + file}
	out, err := c.run(ctx, dir, args...)
	if err != nil {
		return nil, err
	}
	files, err := nulFields(out)
	if err != nil {
		return nil, fmt.Errorf("parsing %s: %w", command(dir, args), err)
	}
	return files, nil
}

// IgnoredDirs returns the directories under dir that git's ignore rules ignore as a whole,
// relative to dir and ending in /, from
// `git ls-files -z --others --ignored --exclude-standard --directory`. That output also names an
// untracked directory whose every file is ignored, though no rule ignores the directory itself;
// Ignored, which does not report one, leaves those out.
func (c Client) IgnoredDirs(ctx context.Context, dir string) ([]string, error) {
	args := []string{"ls-files", "-z", "--others", "--ignored", "--exclude-standard",
		"--directory"}
	out, err := c.run(ctx, dir, args...)
	if err != nil {
		return nil, err
	}
	entries, err := nulFields(out)
	if err != nil {
		return nil, fmt.Errorf("parsing %s: %w", command(dir, args), err)
	}
	var dirs []string
	for _, e := range entries {
		if strings.HasSuffix(e, "/") {
			dirs = append(dirs, e)
		}
	}
	return c.Ignored(ctx, dir, dirs)
}

// checkIgnoreBatch is how many paths one `git check-ignore` call is given, well inside the
// argument limit.
const checkIgnoreBatch = 512

// Ignored returns those of paths, relative to dir, that git ignores, in their order, from
// `git -c core.quotePath=false check-ignore -- <paths…>` run on each batch of them: exit 0 with
// the ignored ones, a line each, and exit 1 when none is. check-ignore takes -z only with --stdin,
// which Runner has none of, so a line git quotes, for a path with a quote, a backslash or a
// control character in it, is unquoted. As for CheckIgnore, a path need not exist, one in an
// ignored directory is ignored, and one git tracks never is; a path beyond a symbolic link is an
// error.
func (c Client) Ignored(ctx context.Context, dir string, paths []string) ([]string, error) {
	var ignored []string
	for batch := range slices.Chunk(paths, checkIgnoreBatch) {
		args := append([]string{"-c", "core.quotePath=false", "check-ignore", "--"}, batch...)
		res, _, err := c.runStatus(ctx, dir, args...)
		if err != nil {
			return nil, err
		}
		for line := range strings.Lines(string(res.Stdout)) {
			line = strings.TrimSuffix(line, "\n")
			if strings.HasPrefix(line, `"`) {
				// git quotes as C does, which strconv.Unquote reads, octal escapes included.
				unquoted, err := strconv.Unquote(line)
				if err != nil {
					return nil, fmt.Errorf("parsing %s: the line %q: %w", command(dir, args),
						line, err)
				}
				line = unquoted
			}
			ignored = append(ignored, line)
		}
	}
	return ignored, nil
}

// SymbolicRef returns the ref that the symbolic ref name points at, shortened, from
// `git symbolic-ref -q --short <name>`, and false when name is not a symbolic ref: it does not
// exist, or it is a detached HEAD. `init` defaults base to refs/remotes/origin/HEAD's target, else
// HEAD's, the current branch (A17).
func (c Client) SymbolicRef(ctx context.Context, dir, name string) (string, bool, error) {
	res, ok, err := c.runStatus(ctx, dir, "symbolic-ref", "-q", "--short", name)
	if err != nil || !ok {
		return "", false, err
	}
	return strings.TrimSuffix(string(res.Stdout), "\n"), true, nil
}

// diffSidesArgs is the call DiffSides makes, before its path: a patch of the worktree against the
// index whose context is the whole file (math.MaxInt32 lines, so one hunk), unaffected by a user's
// external diff, colours or textconv drivers.
var diffSidesArgs = []string{"diff", "--no-ext-diff", "--no-color", "--no-textconv",
	"--unified=2147483647", "--"}

// DiffSides returns the two versions of path, a tracked file relative to the worktree at dir, that
// `git diff` compares: the index's and the worktree's, rebuilt from a patch whose context is the
// whole file. It reports false when git prints no patch of lines for path: the versions are the
// same, or git counts the file binary. The define-order noise rule compares the two
// (docs/design.md, "Slot lifecycle: seed only from a Library current at HEAD, refresh on handout,
// reset without `-x`"). It reads the worktree's file through git rather than directly because the
// remove hook applies that rule from the desktop app's disclaimed helper, where git was the only
// tool tested that could read a repository under ~/Documents (docs/design.md, "One binary, a dozen
// commands, one JSON file per pool", state placement; spike §2 TB).
func (c Client) DiffSides(ctx context.Context, dir, path string) (index, worktree []byte, ok bool,
	err error) {
	args := append(slices.Clone(diffSidesArgs), path)
	out, err := c.run(ctx, dir, args...)
	if err != nil {
		return nil, nil, false, err
	}
	index, worktree, ok, err = parseSides(out)
	if err != nil {
		return nil, nil, false, fmt.Errorf("parsing %s: %w", command(dir, args), err)
	}
	return index, worktree, ok, nil
}

// parseSides rebuilds the two sides of a one-file patch. The lines before the first hunk header
// are the patch's own headers; no hunk means no patch of lines. In the hunk a line starting with a
// space is on both sides, "-" on the old side only and "+" on the new side only; an empty line is
// an empty context line, as diff.suppressBlankEmpty writes it; and "\ No newline at end of file"
// takes the newline off the line before it. A second hunk is an error: the context is the whole
// file.
func parseSides(out []byte) (before, after []byte, ok bool, err error) {
	if len(out) == 0 {
		return nil, nil, false, nil
	}
	if out[len(out)-1] != '\n' {
		return nil, nil, false, errors.New("output does not end in a newline")
	}
	lines := strings.Split(string(out[:len(out)-1]), "\n")
	hunk := slices.IndexFunc(lines, func(l string) bool { return strings.HasPrefix(l, "@@ ") })
	if hunk < 0 {
		return nil, nil, false, nil
	}
	var a, b bytes.Buffer
	var last byte
	for _, l := range lines[hunk+1:] {
		if l == "" {
			l = " "
		}
		switch l[0] {
		case ' ':
			a.WriteString(l[1:] + "\n")
			b.WriteString(l[1:] + "\n")
		case '-':
			a.WriteString(l[1:] + "\n")
		case '+':
			b.WriteString(l[1:] + "\n")
		case '\\':
			if last == 0 || last == '\\' {
				return nil, nil, false, fmt.Errorf("%q follows no line", l)
			}
			if last != '+' {
				a.Truncate(a.Len() - 1)
			}
			if last != '-' {
				b.Truncate(b.Len() - 1)
			}
		case '@':
			return nil, nil, false, fmt.Errorf("a second hunk, %q", l)
		default:
			return nil, nil, false, fmt.Errorf("malformed line %q", l)
		}
		last = l[0]
	}
	return a.Bytes(), b.Bytes(), true, nil
}

// Commit returns the commit that rev names in the repository holding dir, from
// `git rev-parse --verify <rev>^{commit}`, so that a branch, a tag and a SHA compare alike. The
// clone recipe checks a slot's HEAD against base's commit (docs/contracts.md (g) Clone recipe,
// steps 7 and 8). A rev that names no commit is an error carrying git's message.
func (c Client) Commit(ctx context.Context, dir, rev string) (string, error) {
	out, err := c.run(ctx, dir, "rev-parse", "--verify", rev+"^{commit}")
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(string(out), "\n"), nil
}

// The calls below change a worktree. Each is one step of docs/contracts.md (g) Clone recipe, as
// the spike ran it (S§2 TA; S§9 E.1), and the resets after a lease reuse them.

// WorktreeAdd registers a worktree at path, an absolute path, detached at commit and with nothing
// checked out: `git worktree add --no-checkout --detach <path> <commit>`, run in repo, any
// worktree of the repository ((g) step 3). The copy of the seed fills it in ((g) step 4).
func (c Client) WorktreeAdd(ctx context.Context, repo, path, commit string) error {
	_, err := c.run(ctx, repo, "worktree", "add", "--no-checkout", "--detach", path, commit)
	return err
}

// WorktreeAddDetached adds a worktree at path, an absolute path, detached at repo's HEAD and
// checked out: `git -C <repo> worktree add --detach <path>`, what Claude Code makes for a subagent
// itself, which the create hook's subagent branch makes in its place (docs/design.md, "Route
// Claude Code's worktrees through the pool: three branches in one hook").
func (c Client) WorktreeAddDetached(ctx context.Context, repo, path string) error {
	_, err := c.run(ctx, repo, "worktree", "add", "--detach", path)
	return err
}

// WorktreeAddBranch adds a worktree at path, an absolute path, on the branch named after path's
// last element, created from repo's HEAD if it does not exist: `git -C <repo> worktree add
// <path>`, git's default, which the create hook's pass-through branch makes for a repository upool
// does not manage (docs/contracts.md (a), "Telling the kinds apart").
func (c Client) WorktreeAddBranch(ctx context.Context, repo, path string) error {
	_, err := c.run(ctx, repo, "worktree", "add", path)
	return err
}

// WorktreeRemoveClean removes the worktree at path, its registration and its directory, run in
// repo, only if it is clean: `git -C <repo> worktree remove <path>`, without --force, so git
// itself refuses a worktree with changes or untracked files, or one that is locked. The remove
// hook removes a pass-through worktree with it (A22).
func (c Client) WorktreeRemoveClean(ctx context.Context, repo, path string) error {
	_, err := c.run(ctx, repo, "worktree", "remove", path)
	return err
}

// WorktreeRemove removes the worktree at path, its registration and its directory, run in repo:
// `git worktree remove --force <path>`, which removes it even with changes or untracked files.
// `doctor --fix` removes an interrupted clone with it (docs/contracts.md (f) transition 21).
func (c Client) WorktreeRemove(ctx context.Context, repo, path string) error {
	_, err := c.run(ctx, repo, "worktree", "remove", "--force", path)
	return err
}

// WorktreePrune drops the registration of every worktree of the repository at repo whose directory
// is gone: `git worktree prune`. `doctor --fix` runs it before dropping a missing slot's entry
// (docs/contracts.md (f) transition 17).
func (c Client) WorktreePrune(ctx context.Context, repo string) error {
	_, err := c.run(ctx, repo, "worktree", "prune")
	return err
}

// Reset rebuilds the index of the worktree at dir from HEAD and leaves its files alone:
// `git reset -q` ((g) step 6). After WorktreeAdd's --no-checkout the index is empty, so this is
// what makes the copied files tracked again; it took 7.3 to 7.7 s in the spike.
func (c Client) Reset(ctx context.Context, dir string) error {
	_, err := c.run(ctx, dir, "reset", "-q")
	return err
}

// CheckoutFiles reverts every tracked file in the worktree at dir to the index:
// `git checkout -- .` ((g) step 7, for a seed that was dirty).
func (c Client) CheckoutFiles(ctx context.Context, dir string) error {
	_, err := c.run(ctx, dir, "checkout", "--", ".")
	return err
}

// CheckoutPath reverts one tracked file in the worktree at dir to the index:
// `git checkout -- <path>`, which is how define-order noise is reverted (docs/design.md, "Slot
// lifecycle: seed only from a Library current at HEAD, refresh on handout, reset without `-x`").
func (c Client) CheckoutPath(ctx context.Context, dir, path string) error {
	_, err := c.run(ctx, dir, "checkout", "--", path)
	return err
}

// Clean removes the untracked files and directories in the worktree at dir, and keeps the ignored
// ones: `git clean -fd`, without -x, so a gitignored Library survives ((g) step 7; docs/design.md,
// "Slot lifecycle: seed only from a Library current at HEAD, refresh on handout, reset without
// `-x`").
func (c Client) Clean(ctx context.Context, dir string) error {
	_, err := c.run(ctx, dir, "clean", "-fd")
	return err
}

// CheckoutDetach checks out rev in the worktree at dir with HEAD detached:
// `git checkout -q --detach <rev>` ((g) step 7: a slot is detached at base).
func (c Client) CheckoutDetach(ctx context.Context, dir, rev string) error {
	_, err := c.run(ctx, dir, "checkout", "-q", "--detach", rev)
	return err
}

// IsAncestor reports whether the commit ancestor is an ancestor of descendant, or the same commit,
// in the repository holding dir: `git merge-base --is-ancestor <ancestor> <descendant>`, which
// exits 0 for yes and 1 for no. refresh moves a slot only forward, to a commit its HEAD is an
// ancestor of (docs/contracts.md A41).
func (c Client) IsAncestor(ctx context.Context, dir, ancestor, descendant string) (bool, error) {
	_, yes, err := c.runStatus(ctx, dir, "merge-base", "--is-ancestor", ancestor, descendant)
	return yes, err
}

// Fetch fetches branch from remote into the repository holding dir, which updates
// <remote>/<branch>: `git fetch -q <remote> <branch>`. `refresh --fetch` moves ready slots to
// `origin/<base>` after it (docs/contracts.md (f) transition 3).
func (c Client) Fetch(ctx context.Context, dir, remote, branch string) error {
	_, err := c.run(ctx, dir, "fetch", "-q", remote, branch)
	return err
}

// The calls below lease a slot for `upool take`: the branch it names and the slot's checkout of it
// (docs/contracts.md (f) transition 5; A18).

// CheckBranchName reports whether name is a valid branch name, from
// `git check-ref-format --branch <name>`, so that take refuses a bad name before it leases a slot.
func (c Client) CheckBranchName(ctx context.Context, dir, name string) (bool, error) {
	res, err := c.Runner.Run(ctx, dir, c.Env, "check-ref-format", "--branch", name)
	if err != nil {
		return false, err
	}
	return res.ExitCode == 0, nil
}

// BranchExists reports whether the repository holding dir has the local branch name, from
// `git show-ref --verify --quiet refs/heads/<name>`: exit 0 when it does, 1 when it does not.
func (c Client) BranchExists(ctx context.Context, dir, name string) (bool, error) {
	_, ok, err := c.runStatus(ctx, dir, "show-ref", "--verify", "--quiet", "refs/heads/"+name)
	return ok, err
}

// CheckoutBranch checks the existing branch name out in the worktree at dir:
// `git checkout -q <name>`. git refuses a branch checked out in another worktree.
func (c Client) CheckoutBranch(ctx context.Context, dir, name string) error {
	_, err := c.run(ctx, dir, "checkout", "-q", name)
	return err
}

// CheckoutNewBranch creates the branch name at start and checks it out in the worktree at dir:
// `git checkout -q -b <name> <start>`.
func (c Client) CheckoutNewBranch(ctx context.Context, dir, name, start string) error {
	_, err := c.run(ctx, dir, "checkout", "-q", "-b", name, start)
	return err
}

// WorktreeUnlock unlocks the worktree at path, run in repo: `git worktree unlock <path>`.
func (c Client) WorktreeUnlock(ctx context.Context, repo, path string) error {
	_, err := c.run(ctx, repo, "worktree", "unlock", path)
	return err
}

// runStatus runs a call that answers with its exit status, 0 for yes and 1 for no, as
// check-ignore and symbolic-ref -q do. It returns the result and whether the call exited 0; any
// other exit is an error carrying git's stderr.
func (c Client) runStatus(ctx context.Context, dir string, args ...string) (Result, bool, error) {
	res, err := c.Runner.Run(ctx, dir, c.Env, args...)
	if err != nil {
		return Result{}, false, err
	}
	switch res.ExitCode {
	case 0:
		return res, true, nil
	case 1:
		return res, false, nil
	}
	return Result{}, false, exitError(dir, args, res)
}

// exitError is the error for a call that exited with a status its caller does not expect.
func exitError(dir string, args []string, res Result) error {
	msg := strings.TrimSpace(string(res.Stderr))
	return fmt.Errorf("%s: exit status %d: %s", command(dir, args), res.ExitCode, msg)
}

// nulFields splits NUL-terminated output into its fields. Output that does not end in a NUL was cut
// short, and is an error.
func nulFields(out []byte) ([]string, error) {
	if len(out) == 0 {
		return nil, nil
	}
	if out[len(out)-1] != 0 {
		return nil, errors.New("output does not end in a NUL")
	}
	return strings.Split(string(out[:len(out)-1]), "\x00"), nil
}
