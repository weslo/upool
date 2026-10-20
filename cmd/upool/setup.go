package main

import (
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/weslo/upool/assets"
	"github.com/weslo/upool/internal/config"
)

// setupCommand is `upool setup`, run once per machine: it installs the hook scripts under
// ~/.upool/hooks and writes the user config if there is none (docs/design.md, "One binary, a
// dozen commands, one JSON file per pool", the `setup` row). It is idempotent: a second run
// rewrites only what changed, and says what it did to each file.
var setupCommand = command{
	usage: setupUsage,
	setup: func(*flag.FlagSet) handler {
		return func(inv *invocation) (any, []string, error) { return setupMachine(inv) }
	},
}

// setupUsage is setup's usage line.
const setupUsage = "setup [--json]"

// protectedFolders are the folders of a home directory that the desktop app's helper, which runs
// the hooks, cannot read from: under ~/Documents, `bash` could not open a script and `getcwd`
// failed with EPERM (docs/contracts.md (a), "WorktreeCreate output and app behaviour"; spike §7,
// item 1, names all three).
var protectedFolders = []string{"Documents", "Desktop", "Downloads"}

// upoolName is the name setup looks for upool by on PATH.
const upoolName = "upool"

// hooksMode and scriptMode are the modes of ~/.upool/hooks and of the scripts in it: private to
// the user, as the rest of ~/.upool is (internal/statefile), and executable.
const (
	hooksMode  fs.FileMode = 0o700
	scriptMode fs.FileMode = 0o755
)

// change is what setup did to one file or directory.
type change string

const (
	created   change = "created"
	updated   change = "updated"
	unchanged change = "unchanged"
)

// installed is one file or directory setup manages, and what it did to it.
type installed struct {
	Path   string `json:"path"`
	Change change `json:"change"`
}

// setupReport is what setup did: the data of its --json envelope and, in human form, one line per
// file.
type setupReport struct {
	// Upool is the path the hook scripts run upool by: the one setup found this binary through on
	// PATH, symlinks kept.
	Upool string `json:"upool"`
	// Hooks is ~/.upool/hooks.
	Hooks installed `json:"hooks"`
	// Scripts are the hook scripts in it, one per event `upool hook` handles, by name.
	Scripts []installed `json:"scripts"`
	// Config is ~/.config/upool/config.toml, which setup writes only when there is none.
	Config installed `json:"config"`
}

// Human returns one line per file or directory, and the path the scripts run upool by.
func (r setupReport) Human() string {
	lines := []string{describeChange(r.Hooks)}
	for _, s := range r.Scripts {
		lines = append(lines, describeChange(s))
	}
	lines = append(lines, "the hook scripts run "+r.Upool)
	if r.Config.Change == created {
		lines = append(lines, "created "+r.Config.Path)
	} else {
		lines = append(lines, "kept the existing "+r.Config.Path)
	}
	return strings.Join(lines, "\n")
}

// describeChange says what setup did to i.
func describeChange(i installed) string {
	if i.Change == unchanged {
		return i.Path + " is up to date"
	}
	return string(i.Change) + " " + i.Path
}

// setupMachine is setup's handler. It checks the home directory and finds upool on PATH before it
// writes anything, so a refusal leaves the machine as it was.
func setupMachine(inv *invocation) (any, []string, error) {
	if len(inv.args) > 0 {
		return nil, nil, fmt.Errorf("unexpected arguments %q; usage: upool %s", inv.args,
			setupUsage)
	}
	if err := checkHomeReachable(inv.home); err != nil {
		return nil, nil, err
	}
	exe, err := inv.executable()
	if err != nil {
		return nil, nil, fmt.Errorf("finding upool's own binary: %w", err)
	}
	upool, err := findOnPath(inv.getenv("PATH"), exe)
	if err != nil {
		return nil, nil, err
	}
	r := setupReport{Upool: upool}
	hooks := filepath.Join(inv.home, ".upool", "hooks")
	if r.Hooks, err = installHooksDir(hooks); err != nil {
		return nil, nil, err
	}
	names, err := fs.Glob(assets.Hooks, "hooks/*.sh")
	if err != nil {
		return nil, nil, fmt.Errorf("listing the hook scripts: %w", err)
	}
	for _, name := range names {
		template, err := fs.ReadFile(assets.Hooks, name)
		if err != nil {
			return nil, nil, fmt.Errorf("reading the hook script %s: %w", name, err)
		}
		script := strings.Replace(string(template), assets.HookUpool, shellQuote(upool), 1)
		s, err := installScript(filepath.Join(hooks, path.Base(name)), script)
		if err != nil {
			return nil, nil, err
		}
		r.Scripts = append(r.Scripts, s)
	}
	r.Config = installed{Path: config.UserPath(inv.home), Change: unchanged}
	if written, err := config.WriteUserFile(inv.home); err != nil {
		return nil, nil, err
	} else if written {
		r.Config.Change = created
	}
	return r, nil, nil
}

// checkHomeReachable refuses a home directory that is, or is inside, a folder named in
// protectedFolders once its symlinks are resolved: ~/.upool/hooks would then be under one, where
// the helper could not run the scripts (#66). ~ in ~/Documents is the account's home, which may
// not be $HOME, so any such folder counts.
func checkHomeReachable(home string) error {
	if !filepath.IsAbs(home) {
		return fmt.Errorf("home directory %q is not an absolute path", home)
	}
	resolved, err := filepath.EvalSymlinks(home)
	if err != nil {
		return fmt.Errorf("resolving the home directory: %w", err)
	}
	for dir := resolved; ; dir = filepath.Dir(dir) {
		for _, name := range protectedFolders {
			// APFS is case-insensitive by default, so documents is the same folder.
			if strings.EqualFold(filepath.Base(dir), name) {
				return fmt.Errorf("the home directory %s is in %s, which the desktop app's hook "+
					"helper cannot read from, so it could not run the hook scripts under "+
					"~/.upool/hooks: run upool setup with HOME outside %s", resolved, dir,
					strings.Join(protectedFolders, ", "))
			}
		}
		if filepath.Dir(dir) == dir {
			return nil
		}
	}
}

// findOnPath returns the first upool in pathList, a PATH value, that is the binary at exe: the
// path as PATH spells it, not the one its symlinks resolve to. For Homebrew that is
// /opt/homebrew/bin/upool rather than a Cellar path, so `brew upgrade` keeps the scripts working
// (#66); PATH under the helper is not the user's, which is why the scripts name a path at all
// (docs/design.md, "Repository, CI and Homebrew distribution"). An entry that is not absolute is
// skipped, since the helper does not run the scripts from the directory setup ran in.
func findOnPath(pathList, exe string) (string, error) {
	self, err := os.Stat(exe)
	if err != nil {
		return "", fmt.Errorf("finding upool's own binary: %w", err)
	}
	for _, dir := range filepath.SplitList(pathList) {
		if !filepath.IsAbs(dir) {
			continue
		}
		candidate := filepath.Join(dir, upoolName)
		info, err := os.Stat(candidate)
		if err == nil && info.Mode().IsRegular() && os.SameFile(info, self) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("no %s on PATH is this binary, %s, and the hook scripts run upool by "+
		"the path PATH finds it at: run upool setup as the upool on PATH, or put this one's "+
		"directory on PATH", upoolName, exe)
}

// installHooksDir creates dir, ~/.upool/hooks, with hooksMode, creating ~/.upool too if need be,
// or sets an existing one's mode to hooksMode.
func installHooksDir(dir string) (installed, error) {
	info, err := os.Lstat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := os.MkdirAll(dir, hooksMode); err != nil {
			return installed{}, fmt.Errorf("creating %s: %w", dir, err)
		}
		return installed{Path: dir, Change: created}, nil
	case err != nil:
		return installed{}, fmt.Errorf("checking %s: %w", dir, err)
	case !info.IsDir():
		return installed{}, fmt.Errorf("%s is not a directory", dir)
	case info.Mode().Perm() == hooksMode:
		return installed{Path: dir, Change: unchanged}, nil
	}
	if err := os.Chmod(dir, hooksMode); err != nil {
		return installed{}, fmt.Errorf("setting the mode of %s: %w", dir, err)
	}
	return installed{Path: dir, Change: updated}, nil
}

// installScript writes script to file with scriptMode, unless file already holds it with that
// mode. The write goes to a temporary file beside it that is renamed over it, so a hook that runs
// meanwhile runs the old script or the new one, never half of either.
func installScript(file, script string) (_ installed, err error) {
	info, err := os.Lstat(file)
	exists := err == nil
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return installed{}, fmt.Errorf("checking %s: %w", file, err)
	}
	if exists && info.Mode().IsRegular() && info.Mode().Perm() == scriptMode {
		old, err := os.ReadFile(file)
		if err != nil {
			return installed{}, fmt.Errorf("reading %s: %w", file, err)
		}
		if string(old) == script {
			return installed{Path: file, Change: unchanged}, nil
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(file), filepath.Base(file)+".tmp*")
	if err != nil {
		return installed{}, fmt.Errorf("writing %s: %w", file, err)
	}
	defer func() {
		if err != nil {
			// Close fails harmlessly if the file is already closed, and a temporary file that
			// cannot be removed is no reason to hide the error that got here.
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()
	if _, err := tmp.WriteString(script); err != nil {
		return installed{}, fmt.Errorf("writing %s: %w", tmp.Name(), err)
	}
	// Chmod, not CreateTemp's mode, which is 0600, and not a mode the umask can narrow.
	if err := tmp.Chmod(scriptMode); err != nil {
		return installed{}, fmt.Errorf("setting the mode of %s: %w", tmp.Name(), err)
	}
	if err := tmp.Sync(); err != nil {
		return installed{}, fmt.Errorf("syncing %s: %w", tmp.Name(), err)
	}
	if err := tmp.Close(); err != nil {
		return installed{}, fmt.Errorf("closing %s: %w", tmp.Name(), err)
	}
	if err := os.Rename(tmp.Name(), file); err != nil {
		return installed{}, fmt.Errorf("renaming %s to %s: %w", tmp.Name(), file, err)
	}
	if exists {
		return installed{Path: file, Change: updated}, nil
	}
	return installed{Path: file, Change: created}, nil
}

// shellQuote returns s quoted for sh: in single quotes, each single quote in it closed, escaped
// and reopened.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
