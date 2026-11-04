package hooks

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/weslo/upool/internal/paths"
	"github.com/weslo/upool/internal/registry"
)

// LogFile is the hook log's name in ~/.upool/pools/<repo-key>/: two lines for each run of `upool
// hook <event>` whose request belongs to the pool, one as it starts its work and one as it ends, so
// that whether a hook ran, and what it did, has an answer on the machine (#278; docs/contracts.md
// (e), "The hook log"). It sits beside the timings log, which has a line only for an operation
// that reports timings, once it has ended, and the hooks time nothing ((d), "upool's own
// envelope").
const LogFile = "hooks.jsonl"

// The phases of a hook's run that its pool's hook log has a line for.
const (
	PhaseStart = "start"
	PhaseEnd   = "end"
)

// The outcomes an end line records: what the hook did (docs/contracts.md (e), "The hook log").
const (
	// OutcomeSlot is WorktreeCreate's pooled branch handing a session a slot, and
	// OutcomeAgentWorktree and OutcomePassThrough its other two branches making a worktree.
	OutcomeSlot          = "slot"
	OutcomeAgentWorktree = "agent-worktree"
	OutcomePassThrough   = "pass-through"
	// OutcomeNeedsReset and OutcomeParked are WorktreeRemove marking a slot clean or dirty ((f)
	// transitions 8 and 9). OutcomeRemoved and OutcomeKept are its removal of a clean pass-through
	// worktree and its keeping of a dirty one (A22), and OutcomeGone a pass-through worktree
	// already gone from disk, which it leaves.
	OutcomeNeedsReset = "needs-reset"
	OutcomeParked     = "parked"
	OutcomeRemoved    = "removed"
	OutcomeKept       = "kept"
	OutcomeGone       = "gone"
	// OutcomeContext is SessionStart or SubagentStart giving a session in a slot upool's
	// instructions (A43), and OutcomeDeny the lane guard denying a write.
	OutcomeContext = "context"
	OutcomeDeny    = "deny"
	// OutcomeNone is a hook that had nothing to do, such as a session in no slot or a write the lane
	// guard lets through; OutcomeFailed is one that failed before it did anything, its line's error
	// saying why.
	OutcomeNone   = "none"
	OutcomeFailed = "failed"
)

// LogLine is one line of a pool's hook log.
type LogLine struct {
	// Time is when the line was written, PID the hook's process, which pairs a run's two lines
	// when hooks run at once, and Event the hook_event_name of the event the hook handles.
	Time  time.Time `json:"time"`
	PID   int       `json:"pid"`
	Event string    `json:"event"`
	// Phase is PhaseStart or PhaseEnd.
	Phase string `json:"phase"`
	// Kind is what the request is, as its stdin tells (logKind), and Path the path it names
	// (logPath).
	Kind string `json:"kind,omitempty"`
	Path string `json:"path,omitempty"`
	// Outcome, Slot, Worktree and Error are the end line's: what the hook did; the pool's slot it
	// did it to, when there was one; the worktree WorktreeCreate printed; and the error it ended
	// with, when it failed.
	Outcome  string `json:"outcome,omitempty"`
	Slot     string `json:"slot,omitempty"`
	Worktree string `json:"worktree,omitempty"`
	Error    string `json:"error,omitempty"`
}

// Log is one hook run's lines in the hook log of the pool its request belongs to. A nil *Log logs
// nothing, as for a request that belongs to no pool, and for a handler a test calls directly.
type Log struct {
	home, key string
	now       func() time.Time
	stderr    io.Writer
	// line is the start line, with what the handler says it did (did) in place for the end line.
	line LogLine
}

// StartLog appends the start line of the hook for event, run as process pid, to the hook log of
// the pool req belongs to (logPool), before the hook does its work, so that a run that is killed
// still leaves it, and returns the Log the handler records its outcome in and its caller ends. A
// request that belongs to no pool logs nothing, and a failure to log is a warning on env.Stderr:
// the hook's work goes on either way. A hook held before upool's main runs, as macOS can hold a
// process started under ~/Documents in Go's start-up while it asks about access to that folder,
// logs nothing (#286).
func StartLog(env Env, event string, pid int, req Request) *Log {
	start := LogLine{PID: pid, Event: event, Phase: PhaseStart, Kind: logKind(event, req),
		Path: logPath(event, req)}
	key, ok, err := logPool(env.Home, start.Path, req.Cwd)
	if err != nil {
		logWarning(env.Stderr, err)
		return nil
	}
	if !ok {
		return nil
	}
	start.Time = env.Now().UTC()
	if err := appendLogLine(env.Home, key, start); err != nil {
		logWarning(env.Stderr, err)
		return nil
	}
	return &Log{home: env.Home, key: key, now: env.Now, stderr: env.Stderr, line: start}
}

// did records what the hook did, for its end line: outcome, the pool's slot it did it to, and the
// worktree it printed, each "" when there is none.
func (l *Log) did(outcome, slot, worktree string) {
	if l != nil {
		l.line.Outcome, l.line.Slot, l.line.Worktree = outcome, slot, worktree
	}
}

// End appends the hook's end line, once its work is done and err is what it ended with: the outcome
// the handler recorded, or with none OutcomeFailed when err is set and OutcomeNone when it is not,
// and err's message. A failure to log is a warning.
func (l *Log) End(err error) {
	if l == nil {
		return
	}
	end := l.line
	end.Phase, end.Time = PhaseEnd, l.now().UTC()
	if end.Outcome == "" {
		end.Outcome = OutcomeNone
		if err != nil {
			end.Outcome = OutcomeFailed
		}
	}
	if err != nil {
		end.Error = err.Error()
	}
	if err := appendLogLine(l.home, l.key, end); err != nil {
		logWarning(l.stderr, err)
	}
}

// logWarning says on stderr that the hook was not logged, and why.
func logWarning(stderr io.Writer, err error) {
	// stderr is for warnings only; Claude Code reads a hook's answer from stdout.
	_, _ = fmt.Fprintf(stderr, "upool: warning: did not log the hook: %v\n", err)
}

// logKind is what req, a request for event, is, as its stdin tells: for WorktreeCreate who sent it
// (Request.Kind); for WorktreeRemove a desktop session's when its transcript_path is empty, as on
// its create, and otherwise a CLI session's, no subagent's ever coming (docs/contracts.md (a),
// "WorktreeRemove fields"); for SessionStart its source ((b), "SessionStart sources"); for
// SubagentStart a subagent; and for PreToolUse the tool.
func logKind(event string, req Request) string {
	switch event {
	case EventWorktreeCreate:
		return string(req.Kind())
	case EventWorktreeRemove:
		if req.TranscriptPath == "" {
			return string(DesktopSession)
		}
		return string(CLISession)
	case EventSessionStart:
		return req.Source
	case EventSubagentStart:
		return string(Subagent)
	case EventPreToolUse:
		return req.ToolName
	}
	return ""
}

// logPath is the path req, a request for event, names: WorktreeRemove's worktree_path, the file a
// PreToolUse tool writes, made absolute against cwd as the lane guard makes it, and otherwise the
// request's cwd, the checkout a WorktreeCreate asks a worktree of or where a session starts.
func logPath(event string, req Request) string {
	switch event {
	case EventWorktreeRemove:
		return req.WorktreePath
	case EventPreToolUse:
		path := req.ToolInput.FilePath
		if path == "" {
			path = req.ToolInput.NotebookPath
		}
		if path != "" && !filepath.IsAbs(path) && filepath.IsAbs(req.Cwd) {
			path = filepath.Join(req.Cwd, path)
		}
		if path != "" {
			return path
		}
	}
	return req.Cwd
}

// logPool returns the key of the pool whose hook log gets a hook's lines: that of the registered
// repository whose main checkout is path or contains it, a slot or a worktree under it included,
// the innermost where registered repositories nest; failing that, that of the one that contains
// cwd; and whether there is one. Like registry.Containing, it compares internal/paths keys, which
// needs no git call and opens no file under the repository, so that it works from the desktop
// app's restricted helper (docs/contracts.md (a)).
func logPool(home, path, cwd string) (string, bool, error) {
	repos, err := registry.List(home)
	if err != nil || len(repos) == 0 {
		return "", false, err
	}
	for _, p := range []string{path, cwd} {
		if !filepath.IsAbs(p) {
			continue
		}
		k, err := paths.Key(p)
		if err != nil {
			return "", false, err
		}
		var found registry.Repo
		ok := false
		for _, repo := range repos {
			if paths.Within(k, repo.Path) && (!ok || len(repo.Path) > len(found.Path)) {
				found, ok = repo, true
			}
		}
		if ok {
			return found.Key, true, nil
		}
	}
	return "", false, nil
}

// logFile returns the hook log of the pool key under home, and its name as messages give it.
func logFile(home, key string) (path, shown string) {
	return filepath.Join(home, ".upool", "pools", key, LogFile),
		"~/.upool/pools/" + key + "/" + LogFile
}

// appendLogLine appends line to the hook log of the pool key under home, in one write to the file
// opened for appending, so that the lines of hooks that run at once do not interleave, and without
// ~/.upool/lock, which a hook may be waiting for: nothing reads the log back to rewrite it, as for
// the timings log. It creates the log but never the pool's directory, which only the pool's state
// file makes, so that a hook that runs after `upool deinit` removed its pool leaves nothing behind
// (docs/contracts.md (e), "The hook log").
func appendLogLine(home, key string, line LogLine) error {
	b, err := json.Marshal(line)
	if err != nil {
		return fmt.Errorf("encoding the hook log's line: %w", err)
	}
	path, shown := logFile(home, key)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("opening %s: %w", shown, err)
	}
	_, err = f.Write(append(b, '\n'))
	if err = errors.Join(err, f.Close()); err != nil {
		return fmt.Errorf("appending to %s: %w", shown, err)
	}
	return nil
}

// maxLogLine is the longest line ReadLog reads; a hook's line, its error included, is far shorter.
const maxLogLine = 1 << 20

// ReadLog returns the lines of the hook log of the pool key under home, in the order they were
// appended, which is the order the hooks wrote them. A missing log has none. A line that does not
// decode, such as one cut short by a full disk, is passed over, so that one bad line hides none of
// the others.
func ReadLog(home, key string) ([]LogLine, error) {
	path, shown := logFile(home, key)
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", shown, err)
	}
	defer func() { _ = f.Close() }() // read-only
	var lines []LogLine
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), maxLogLine)
	for sc.Scan() {
		var l LogLine
		if json.Unmarshal(sc.Bytes(), &l) == nil {
			lines = append(lines, l)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("reading %s: %w", shown, err)
	}
	return lines, nil
}
