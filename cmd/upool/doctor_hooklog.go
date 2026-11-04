package main

import (
	"fmt"
	"time"

	"github.com/weslo/upool/internal/desktop"
	"github.com/weslo/upool/internal/hooks"
	"github.com/weslo/upool/internal/paths"
	"github.com/weslo/upool/internal/pool"
)

// checkHookLog is the name of the lines that report, from the pool's hook log, the last
// WorktreeRemove of each slot the desktop app's record no longer names (#278).
const checkHookLog = "hook-log"

// hookLogChecks reports, for each of slots, the pool key's slots as doctor read them, that rec, the
// desktop app's record of its sessions' worktrees, does not name, the last WorktreeRemove the
// pool's hook log has for it, so that whether the app ran upool's remove hook when it let the
// slot go, and what the hook did, has an answer (#278; docs/contracts.md (e), "The hook log"). A
// slot the log has no handout to a desktop session and no WorktreeRemove for gets no line. With
// the record unknown, as without the app, nothing says which slots it no longer names, and nothing
// is reported. reclaimed is what doctor --fix reclaimed, by slot name, after it read slots: a lease
// it ended, or a slot it dropped, is reported as reclaimed, not as still on the slot.
func hookLogChecks(home, key string, slots []pool.Slot, rec desktop.Record,
	reclaimed map[string]pool.Reclaimed) []doctorCheck {
	if !rec.Known || len(slots) == 0 {
		return nil
	}
	lines, err := hooks.ReadLog(home, key)
	if err != nil {
		return []doctorCheck{failedCheck(checkHookLog, err)}
	}
	if len(lines) == 0 {
		return nil
	}
	keys := logKeys{}
	var checks []doctorCheck
	for _, s := range slots {
		if _, held := rec.Holds(s.Path); held {
			continue
		}
		if c, ok := hookLogCheck(s, lines, keys, reclaimed); ok {
			checks = append(checks, c)
		}
	}
	return checks
}

// hookLogCheck is s's line from the hook log's lines, if it has one: its last WorktreeRemove and
// what that did, or, when the log has a desktop session handed s since, that no WorktreeRemove
// came for that session. The slot's lease tells whether that session's lease is still on it: a
// lease acquired no later than the handout ended is the one the handout took, unless reclaimed
// says doctor --fix has since reclaimed it, its holder dead ((f) transitions 13, 14 and 24).
func hookLogCheck(s pool.Slot, lines []hooks.LogLine, keys logKeys,
	reclaimed map[string]pool.Reclaimed) (doctorCheck, bool) {
	want, err := paths.Key(s.Path)
	if err != nil {
		return failedCheck(checkHookLog, err), true
	}
	handout, start, end := -1, -1, -1
	for i, l := range lines {
		switch {
		case l.Event == hooks.EventWorktreeCreate && l.Phase == hooks.PhaseEnd &&
			l.Outcome == hooks.OutcomeSlot && l.Kind == string(hooks.DesktopSession) &&
			keys.is(l.Worktree, want):
			handout = i
		case l.Event == hooks.EventWorktreeRemove && l.Phase == hooks.PhaseStart &&
			keys.is(l.Path, want):
			start, end = i, -1
		case l.Event == hooks.EventWorktreeRemove && l.Phase == hooks.PhaseEnd && start >= 0 &&
			l.PID == lines[start].PID && keys.is(l.Path, want):
			end = i
		}
	}
	line := func(result checkResult, format string, args ...any) (doctorCheck, bool) {
		return doctorCheck{checkHookLog, result, s.Name + ": " + fmt.Sprintf(format, args...)},
			true
	}
	switch {
	case handout > start:
		at := logTime(lines[handout].Time)
		l := s.Lease
		rc, fixed := reclaimed[s.Name]
		switch {
		case l == nil || l.AcquiredAt.After(lines[handout].Time):
			return line(checkPass, "no WorktreeRemove is logged since a desktop session was "+
				"handed it at %s; that session's lease has ended since", at)
		case fixed && rc.Err == nil:
			return line(checkPass, "no WorktreeRemove is logged since a desktop session was "+
				"handed it at %s; its holder, process %d, was gone, and doctor --fix has just "+
				"reclaimed its lease", at, rc.HolderPID)
		}
		return line(checkWarn, "the desktop app no longer records it, and no WorktreeRemove "+
			"is logged since a desktop session was handed it at %s: the app ran no remove "+
			"hook for that session, or one that never reached upool's own code, and its "+
			"lease is still on the slot", at)
	case start < 0:
		return doctorCheck{}, false
	case end < 0:
		return line(checkWarn, "its last WorktreeRemove started at %s and logged no end: it was "+
			"killed, or is still running", logTime(lines[start].Time))
	case lines[end].Error != "":
		return line(checkWarn, "its last WorktreeRemove ran at %s, with outcome %s: %s",
			logTime(lines[start].Time), lines[end].Outcome, lines[end].Error)
	}
	return line(checkPass, "its last WorktreeRemove ran at %s, with outcome %s",
		logTime(lines[start].Time), lines[end].Outcome)
}

// logKeys caches the internal/paths key of each path the hook log names, which the lines of every
// slot compare against, so that each is resolved once.
type logKeys map[string]string

// is reports whether path, as the log names it, has the key want; a path with no key has none.
func (k logKeys) is(path, want string) bool {
	if path == "" {
		return false
	}
	key, ok := k[path]
	if !ok {
		key, _ = paths.Key(path) // a path with no key matches no slot
		k[path] = key
	}
	return key == want
}

// logTime is how doctor gives a time from the hook log: UTC, to the second.
func logTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}
