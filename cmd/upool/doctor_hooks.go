package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/weslo/upool/assets"
	"github.com/weslo/upool/internal/registry"
)

// The names of the checks of what `upool setup` installs for the hooks (#69).
const (
	checkHookScripts    = "hook-scripts"
	checkHookSettings   = "hook-settings"
	checkHookProvenance = "hook-provenance"
)

// hookChecks checks the hook installation: the scripts in ~/.upool/hooks, their registration in
// ~/.claude/settings.json, and that no registered repository registers them in its own settings
// as well (docs/design.md, "One binary, a dozen commands, one JSON file per pool", the `doctor`
// row; docs/contracts.md (a), "WorktreeCreate output and app behaviour").
func hookChecks(home string) []doctorCheck {
	hooksDir := filepath.Join(home, ".upool", "hooks")
	return []doctorCheck{hookScriptsCheck(hooksDir), hookSettingsCheck(home, hooksDir),
		hookProvenanceCheck(home)}
}

// hookScriptsCheck fails unless each hook script setup installs is in hooksDir, executable,
// outside the folders the desktop app's hook helper cannot read from, and runs an upool that
// exists.
func hookScriptsCheck(hooksDir string) doctorCheck {
	names, err := fs.Glob(assets.Hooks, "hooks/*.sh")
	if err != nil {
		return failedCheck(checkHookScripts, fmt.Errorf("listing the hook scripts: %w", err))
	}
	var problems, upools []string
	for _, name := range names {
		template, err := fs.ReadFile(assets.Hooks, name)
		if err != nil {
			return failedCheck(checkHookScripts, fmt.Errorf("reading the hook script %s: %w",
				name, err))
		}
		upool, problem := checkHookScript(filepath.Join(hooksDir, path.Base(name)),
			string(template))
		if problem != "" {
			problems = append(problems, problem)
		} else if !slices.Contains(upools, upool) {
			upools = append(upools, upool)
		}
	}
	if len(problems) > 0 {
		return doctorCheck{checkHookScripts, checkFail, strings.Join(problems, "; ")}
	}
	return doctorCheck{checkHookScripts, checkPass, fmt.Sprintf("the %d hook scripts in %s are "+
		"executable, outside %s, and run %s", len(names), hooksDir,
		strings.Join(protectedFolders, ", "), strings.Join(upools, ", "))}
}

// checkHookScript checks one hook script, file, against the template setup fills in to write it.
// It returns the upool the script runs, or what is wrong with it.
func checkHookScript(file, template string) (upool, problem string) {
	info, err := os.Stat(file)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "", file + " is missing: run upool setup"
	case err != nil:
		return "", fmt.Sprintf("checking %s: %v", file, err)
	case !info.Mode().IsRegular():
		return "", file + " is not a file"
	case info.Mode().Perm()&0o100 == 0:
		return "", file + " is not executable: run upool setup"
	}
	resolved, err := filepath.EvalSymlinks(file)
	if err != nil {
		return "", fmt.Sprintf("resolving %s: %v", file, err)
	}
	if dir, ok := protectedFolder(resolved); ok {
		return "", fmt.Sprintf("%s is in %s, which the desktop app's hook helper cannot read "+
			"from", resolved, dir)
	}
	script, err := os.ReadFile(file)
	if err != nil {
		return "", fmt.Sprintf("reading %s: %v", file, err)
	}
	upool, ok := scriptUpool(string(script), template)
	if !ok {
		return "", file + " is not the script upool setup writes: run upool setup"
	}
	info, err = os.Stat(upool)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "", fmt.Sprintf("%s runs %s, which does not exist: run upool setup as the upool "+
			"on PATH", file, upool)
	case err != nil:
		return "", fmt.Sprintf("checking %s, which %s runs: %v", upool, file, err)
	case !info.Mode().IsRegular() || info.Mode().Perm()&0o100 == 0:
		return "", fmt.Sprintf("%s runs %s, which is not an executable file", file, upool)
	}
	return upool, ""
}

// scriptUpool returns the upool script runs, if script is template with assets.HookUpool replaced
// by an absolute path quoted for sh, as setup writes it.
func scriptUpool(script, template string) (string, bool) {
	prefix, suffix, ok := strings.Cut(template, assets.HookUpool)
	if !ok || len(script) < len(prefix)+len(suffix) || !strings.HasPrefix(script, prefix) ||
		!strings.HasSuffix(script, suffix) {
		return "", false
	}
	upool, ok := shellUnquote(script[len(prefix) : len(script)-len(suffix)])
	return upool, ok && filepath.IsAbs(upool)
}

// shellUnquote reverses shellQuote: it returns the string q quotes, if q is in that form.
func shellUnquote(q string) (string, bool) {
	if len(q) < 2 || q[0] != '\'' || q[len(q)-1] != '\'' {
		return "", false
	}
	parts := strings.Split(q[1:len(q)-1], `'\''`)
	for _, part := range parts {
		if strings.Contains(part, "'") {
			return "", false
		}
	}
	return strings.Join(parts, "'"), true
}

// hookSettingsCheck fails unless ~/.claude/settings.json registers each script in hooksDir for
// its event, the only scope the desktop app vouches for (docs/contracts.md (a)).
func hookSettingsCheck(home, hooksDir string) doctorCheck {
	file := settingsPath(home)
	b, err := os.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		return doctorCheck{checkHookSettings, checkFail, file + " does not exist, so no hook " +
			"is registered: run upool setup"}
	}
	if err != nil {
		return failedCheck(checkHookSettings, fmt.Errorf("reading %s: %w", file, err))
	}
	registered, err := registeredHooks(b)
	if err != nil {
		return failedCheck(checkHookSettings, fmt.Errorf("reading %s: %w", file, err))
	}
	var missing, events []string
	for _, h := range hookEvents {
		script := filepath.Join(hooksDir, h.script)
		events = append(events, h.event)
		if !slices.ContainsFunc(registered[h.event], func(c hookCommand) bool {
			return c.Type == "command" && filepath.Clean(c.Command) == script
		}) {
			missing = append(missing, fmt.Sprintf("%s for %s", h.script, h.event))
		}
	}
	if len(missing) > 0 {
		return doctorCheck{checkHookSettings, checkFail, fmt.Sprintf("%s does not register %s: "+
			"run upool setup", file, strings.Join(missing, ", "))}
	}
	return doctorCheck{checkHookSettings, checkPass, fmt.Sprintf("%s registers the hook "+
		"scripts for %s", file, strings.Join(events, ", "))}
}

// hookProvenanceCheck warns about each registered repository whose own .claude/settings*.json
// registers upool's hooks. The user-scope hooks already run in every repository, and from a
// project's settings the desktop app does not vouch for a hook ("provenance not vouched", spike
// §7).
func hookProvenanceCheck(home string) doctorCheck {
	repos, err := registry.List(home)
	if err != nil {
		return failedCheck(checkHookProvenance, err)
	}
	var found, unread []string
	for _, repo := range repos {
		dir := filepath.Join(repo.Path, ".claude")
		entries, err := os.ReadDir(dir)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			unread = append(unread, fmt.Sprintf("%s: %v", dir, err))
			continue
		}
		for _, entry := range entries {
			if ok, _ := filepath.Match("settings*.json", entry.Name()); !ok || entry.IsDir() {
				continue
			}
			file := filepath.Join(dir, entry.Name())
			b, err := os.ReadFile(file)
			if err != nil {
				unread = append(unread, fmt.Sprintf("%s: %v", file, err))
				continue
			}
			registered, err := registeredHooks(b)
			if err != nil {
				unread = append(unread, fmt.Sprintf("%s: %v", file, err))
				continue
			}
			if registersUpool(registered) {
				found = append(found, file)
			}
		}
	}
	var reasons []string
	if len(found) > 0 {
		reasons = append(reasons, strings.Join(found, ", ")+" registers upool's hooks, which "+
			"~/.claude/settings.json already registers for every repository and the desktop app "+
			"does not vouch for from a project's settings: remove them there")
	}
	if len(unread) > 0 {
		reasons = append(reasons, "could not read "+strings.Join(unread, "; "))
	}
	if len(reasons) > 0 {
		return doctorCheck{checkHookProvenance, checkWarn, strings.Join(reasons, "; ")}
	}
	return doctorCheck{checkHookProvenance, checkPass, fmt.Sprintf("none of the %d registered "+
		"repositories registers upool's hooks in its own .claude/settings*.json", len(repos))}
}

// hookCommand is the part of a hook handler the hook checks read.
type hookCommand struct {
	Type    string   `json:"type"`
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

// registeredHooks returns the handlers a settings file registers, by event. A group or handler
// of a shape it does not recognise is passed over; only a file that is not a JSON object, or
// whose hooks are not one, is an error.
func registeredHooks(settings []byte) (map[string][]hookCommand, error) {
	var doc struct {
		Hooks map[string]json.RawMessage `json:"hooks"`
	}
	if err := json.Unmarshal(settings, &doc); err != nil {
		return nil, err
	}
	registered := map[string][]hookCommand{}
	for event, raw := range doc.Hooks {
		var groups []json.RawMessage
		if json.Unmarshal(raw, &groups) != nil {
			continue
		}
		for _, g := range groups {
			var group struct {
				Hooks []json.RawMessage `json:"hooks"`
			}
			if json.Unmarshal(g, &group) != nil {
				continue
			}
			for _, h := range group.Hooks {
				var c hookCommand
				if json.Unmarshal(h, &c) == nil {
					registered[event] = append(registered[event], c)
				}
			}
		}
	}
	return registered, nil
}

// registersUpool says whether any of the handlers runs upool's hooks: a script in a .upool/hooks
// directory, however its path is spelled, or `upool hook`.
func registersUpool(registered map[string][]hookCommand) bool {
	for _, handlers := range registered {
		for _, c := range handlers {
			words := append(strings.Fields(c.Command), c.Args...)
			for _, w := range words {
				if strings.Contains(w, "/.upool/hooks/") {
					return true
				}
			}
			if len(words) > 1 && filepath.Base(words[0]) == upoolName && words[1] == "hook" {
				return true
			}
		}
	}
	return false
}
