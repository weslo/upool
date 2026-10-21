package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/weslo/upool/internal/registry"
)

// The names of the checks of what `upool setup` installs for the hooks (#69).
const (
	checkHookSettings   = "hook-settings"
	checkHookProvenance = "hook-provenance"
)

// hookChecks checks the hook installation: its registration in ~/.claude/settings.json, and that no
// registered repository registers upool's hooks in its own settings as well (docs/design.md, "One
// binary, a dozen commands, one JSON file per pool", the `doctor` row; docs/contracts.md (a),
// "WorktreeCreate output and app behaviour").
func hookChecks(home string) []doctorCheck {
	return []doctorCheck{hookSettingsCheck(home), hookProvenanceCheck(home)}
}

// hookSettingsCheck fails unless ~/.claude/settings.json, the only scope the desktop app vouches
// for (docs/contracts.md (a)), registers upool for each event as setup writes it, `upool hook
// <event>` in exec form with upool an absolute path, and each upool it registers is an executable
// file.
func hookSettingsCheck(home string) doctorCheck {
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
	var missing, events, upools []string
	for _, h := range hookEvents {
		events = append(events, h.event)
		i := slices.IndexFunc(registered[h.event], func(c commandHook) bool {
			return c.Type == "command" && filepath.IsAbs(c.Command) &&
				filepath.Base(c.Command) == upoolName &&
				slices.Equal(c.Args, []string{"hook", h.hook})
		})
		if i < 0 {
			missing = append(missing, fmt.Sprintf("upool hook %s for %s", h.hook, h.event))
		} else if upool := registered[h.event][i].Command; !slices.Contains(upools, upool) {
			upools = append(upools, upool)
		}
	}
	if len(missing) > 0 {
		return doctorCheck{checkHookSettings, checkFail, fmt.Sprintf("%s does not register %s: "+
			"run upool setup", file, strings.Join(missing, ", "))}
	}
	for _, upool := range upools {
		info, err := os.Stat(upool)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return doctorCheck{checkHookSettings, checkFail, fmt.Sprintf("%s registers %s, which "+
				"does not exist: run upool setup as the upool on PATH", file, upool)}
		case err != nil:
			return failedCheck(checkHookSettings, fmt.Errorf("checking %s, which %s registers: %w",
				upool, file, err))
		case !info.Mode().IsRegular() || info.Mode().Perm()&0o100 == 0:
			return doctorCheck{checkHookSettings, checkFail, fmt.Sprintf("%s registers %s, which "+
				"is not an executable file: run upool setup as the upool on PATH", file, upool)}
		}
	}
	return doctorCheck{checkHookSettings, checkPass, fmt.Sprintf("%s registers %s for %s", file,
		strings.Join(upools, ", "), strings.Join(events, ", "))}
}

// hookProvenanceCheck warns about each registered repository whose committed .claude/settings
// files register upool's hooks. init registers them in the project's .claude/settings.local.json,
// which holds this machine's path to upool and stays out of git (A26), so that file is passed over;
// a committed copy would run for everyone who clones the repository.
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
			if ok, _ := filepath.Match("settings*.json", entry.Name()); !ok || entry.IsDir() ||
				entry.Name() == filepath.Base(projectSettings) {
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
			"init registers in the project's .claude/settings.local.json: remove them there")
	}
	if len(unread) > 0 {
		reasons = append(reasons, "could not read "+strings.Join(unread, "; "))
	}
	if len(reasons) > 0 {
		return doctorCheck{checkHookProvenance, checkWarn, strings.Join(reasons, "; ")}
	}
	return doctorCheck{checkHookProvenance, checkPass, fmt.Sprintf("none of the %d registered "+
		"repositories registers upool's hooks in a committed .claude/settings file", len(repos))}
}

// registeredHooks returns the handlers a settings file registers, by event. A group or handler
// of a shape it does not recognise is passed over; only a file that is not a JSON object, or
// whose hooks are not one, is an error.
func registeredHooks(settings []byte) (map[string][]commandHook, error) {
	var doc struct {
		Hooks map[string]json.RawMessage `json:"hooks"`
	}
	if err := json.Unmarshal(settings, &doc); err != nil {
		return nil, err
	}
	registered := map[string][]commandHook{}
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
				var c commandHook
				if json.Unmarshal(h, &c) == nil {
					registered[event] = append(registered[event], c)
				}
			}
		}
	}
	return registered, nil
}

// registersUpool says whether any of the handlers runs `upool hook`.
func registersUpool(registered map[string][]commandHook) bool {
	for _, handlers := range registered {
		if slices.ContainsFunc(handlers, runsUpoolHook) {
			return true
		}
	}
	return false
}
