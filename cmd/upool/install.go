package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/weslo/upool/internal/block"
)

// The files init installs in a project beside the hooks it registers there: the upool skill and
// the instruction blocks (A26; docs/design.md, "Hooks route, skills teach, leases and lanes keep
// agents from colliding", the three layers), and the helpers that write them.

// protectedFolders are the folders of a home directory that the desktop app's helper, which runs
// the hooks, cannot read a file in: under ~/Documents, `bash` could not open a script and `head`
// failed with EPERM, though a binary there ran (docs/contracts.md (a), "WorktreeCreate output and
// app behaviour"; spike §7, item 1, names all three).
var protectedFolders = []string{"Documents", "Desktop", "Downloads"}

// upoolName is the name init looks for upool by on PATH.
const upoolName = "upool"

// skillMode is the mode of an installed SKILL.md.
const skillMode fs.FileMode = 0o644

// skillFiles are where init installs the skill, relative to the repository: Claude Code's project
// skills, and the shared directory other agents read skills from (docs/design.md, "Hooks route,
// skills teach, leases and lanes keep agents from colliding"). The confirm's agent-file copy
// carries both into the slots.
var skillFiles = []string{".claude/skills/upool/SKILL.md", ".agents/skills/upool/SKILL.md"}

// change is what init did to one file.
type change string

const (
	created   change = "created"
	updated   change = "updated"
	unchanged change = "unchanged"
)

// installed is one file init manages, and what it did to it.
type installed struct {
	Path   string `json:"path"`
	Change change `json:"change"`
}

// describeChange says what init did to i.
func describeChange(i installed) string {
	if i.Change == unchanged {
		return i.Path + " is up to date"
	}
	return string(i.Change) + " " + i.Path
}

// checkHomeReachable refuses a home directory that is, or is inside, a folder named in
// protectedFolders once its symlinks are resolved: ~/.upool would then be under one, where the
// helper could not read the state the hooks need (#66). ~ in ~/Documents is the account's home,
// which may not be $HOME, so any such folder counts.
func checkHomeReachable(home string) error {
	if !filepath.IsAbs(home) {
		return fmt.Errorf("home directory %q is not an absolute path", home)
	}
	resolved, err := filepath.EvalSymlinks(home)
	if err != nil {
		return fmt.Errorf("resolving the home directory: %w", err)
	}
	if dir, ok := protectedFolder(resolved); ok {
		return fmt.Errorf("the home directory %s is in %s, which the desktop app's hook helper "+
			"cannot read from, so the hooks could not read upool's state under ~/.upool: run "+
			"upool init with HOME outside %s", resolved, dir,
			strings.Join(protectedFolders, ", "))
	}
	return nil
}

// protectedFolder returns the folder named in protectedFolders that path, symlinks resolved, is
// or is inside, if there is one.
func protectedFolder(path string) (string, bool) {
	for dir := path; ; dir = filepath.Dir(dir) {
		for _, name := range protectedFolders {
			// APFS is case-insensitive by default, so documents is the same folder.
			if strings.EqualFold(filepath.Base(dir), name) {
				return dir, true
			}
		}
		if filepath.Dir(dir) == dir {
			return "", false
		}
	}
}

// findOnPath returns the first upool in pathList, a PATH value, that is the binary at exe: the
// path as PATH spells it, not the one its symlinks resolve to. For Homebrew that is
// /opt/homebrew/bin/upool rather than a Cellar path, so `brew upgrade` keeps the hooks working
// (#66); PATH under the helper is not the user's, which is why the hooks name a path at all
// (docs/design.md, "Repository, CI and Homebrew distribution"). An entry that is not absolute is
// skipped, since the helper does not run the hooks from the directory init ran in.
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
	return "", fmt.Errorf("no %s on PATH is this binary, %s, and the hooks run upool by the "+
		"path PATH finds it at: run the upool on PATH, or put this one's directory on PATH",
		upoolName, exe)
}

// checkBlock refuses an instruction file that upool's block b cannot be upserted into, such as one
// whose markers do not pair, without writing it. A missing file is fine: the upsert creates it.
func checkBlock(file string, b block.Block) error {
	doc, err := os.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading %s: %w", file, err)
	}
	if _, _, err := block.Apply(doc, b); err != nil {
		return fmt.Errorf("upserting upool's block into %s: %w", file, err)
	}
	return nil
}

// installFile writes content to file with mode, unless file already holds it with that mode.
func installFile(file, content string, mode fs.FileMode) (installed, error) {
	info, err := os.Lstat(file)
	exists := err == nil
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return installed{}, fmt.Errorf("checking %s: %w", file, err)
	}
	if exists && info.Mode().IsRegular() && info.Mode().Perm() == mode {
		old, err := os.ReadFile(file)
		if err != nil {
			return installed{}, fmt.Errorf("reading %s: %w", file, err)
		}
		if string(old) == content {
			return installed{Path: file, Change: unchanged}, nil
		}
	}
	if err := writeAtomic(file, []byte(content), mode); err != nil {
		return installed{}, err
	}
	if exists {
		return installed{Path: file, Change: updated}, nil
	}
	return installed{Path: file, Change: created}, nil
}

// writeAtomic writes data to file with mode. The write goes to a temporary file beside it that is
// renamed over it, so a hook or a Claude Code session that reads it meanwhile reads the old file
// or the new one, never half of either.
func writeAtomic(file string, data []byte, mode fs.FileMode) error {
	tmp, err := writeTemp(file, data, mode)
	if err != nil {
		return err
	}
	if err := os.Rename(tmp, file); err != nil {
		// A temporary file that cannot be removed is no reason to hide the error that got here.
		_ = os.Remove(tmp)
		return fmt.Errorf("renaming %s to %s: %w", tmp, file, err)
	}
	return nil
}

// writeTemp writes data with mode to a new temporary file beside file, synced, and returns its
// name.
func writeTemp(file string, data []byte, mode fs.FileMode) (_ string, err error) {
	tmp, err := os.CreateTemp(filepath.Dir(file), filepath.Base(file)+".tmp*")
	if err != nil {
		return "", fmt.Errorf("writing %s: %w", file, err)
	}
	defer func() {
		if err != nil {
			// Close fails harmlessly if the file is already closed, and a temporary file that
			// cannot be removed is no reason to hide the error that got here.
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		return "", fmt.Errorf("writing %s: %w", tmp.Name(), err)
	}
	// Chmod, not CreateTemp's mode, which is 0600, and not a mode the umask can narrow.
	if err := tmp.Chmod(mode); err != nil {
		return "", fmt.Errorf("setting the mode of %s: %w", tmp.Name(), err)
	}
	if err := tmp.Sync(); err != nil {
		return "", fmt.Errorf("syncing %s: %w", tmp.Name(), err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("closing %s: %w", tmp.Name(), err)
	}
	return tmp.Name(), nil
}
