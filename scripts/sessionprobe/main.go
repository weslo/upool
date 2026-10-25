// Command sessionprobe is the SessionStart hook of #228's spike, and its SubagentStart hook: it
// records the hook's stdin, then prints a context that holds a marker, under the event that ran it, so that a session asked for the marker shows whether the
// context reached the model. Build it outside ~/Documents and register it, in exec form, as a
// scratch repository's SessionStart hook in its .claude/settings.local.json:
//
//	go build -o ~/.upool-probe/sessionprobe ./scripts/sessionprobe
//
// It reads its settings from files in ~/.upool-probe (UPOOL_PROBE_DIR overrides the folder), so a
// run changes them without touching the hook's registration:
//
//   - mode: json, the marker as hookSpecificOutput.additionalContext; plain, the marker as plain
//     stdout; big, json with filler.txt's text before the marker, to show whether a long context
//     arrives whole; or none, nothing on stdout. json when the file is missing.
//   - marker: the word the context names, followed by "-" and stdin's source (startup, resume,
//     clear or compact), so that an answer shows which session start's context the model saw;
//     none when the file is missing.
//   - copy-claude-md: when present, the hook first copies the main checkout's CLAUDE.md into the
//     session's worktree (stdin's cwd), to show whether Claude Code reads CLAUDE.md after the hook
//     has run. The main checkout is the path in the file main; in a `claude -p --worktree` session
//     $CLAUDE_PROJECT_DIR did not name it.
//
// Each stdin is saved as captures/hook-<time>-<pid>.json, and log.txt gets a line per run
// naming what it printed. Replace their paths with scripts/scrubpaths before adding any to
// testdata/captures/hooks/.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "sessionprobe:", err)
		os.Exit(1)
	}
}

func run() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	dir := os.Getenv("UPOOL_PROBE_DIR")
	if dir == "" {
		dir = filepath.Join(home, ".upool-probe")
	}
	if err := os.MkdirAll(filepath.Join(dir, "captures"), 0o755); err != nil {
		return err
	}
	stdin, err := io.ReadAll(os.Stdin)
	if err != nil {
		return err
	}
	stamp := fmt.Sprintf("%s-%d", time.Now().Format("20060102T150405"), os.Getpid())
	capture := filepath.Join(dir, "captures", "hook-"+stamp+".json")
	if err := os.WriteFile(capture, stdin, 0o644); err != nil {
		return err
	}
	var in struct {
		Event  string `json:"hook_event_name"`
		CWD    string `json:"cwd"`
		Source string `json:"source"`
	}
	if err := json.Unmarshal(stdin, &in); err != nil {
		return fmt.Errorf("decoding stdin: %w", err)
	}
	if in.Event == "" {
		in.Event = "SessionStart"
	}
	if in.Source == "" {
		in.Source = strings.ToLower(in.Event)
	}
	mode, marker := setting(dir, "mode", "json"), setting(dir, "marker", "none")
	copied := "no"
	project := setting(dir, "main", "")
	if _, err := os.Stat(filepath.Join(dir, "copy-claude-md")); err == nil && project != "" &&
		in.CWD != "" && in.CWD != project {
		b, err := os.ReadFile(filepath.Join(project, "CLAUDE.md"))
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(in.CWD, "CLAUDE.md"), b, 0o644); err != nil {
			return err
		}
		copied = "yes"
	}
	text := "The upool probe word is " + marker + "-" + in.Source + ". If you are asked for the " +
		"probe word, reply with it exactly."
	if mode == "big" {
		filler, err := os.ReadFile(filepath.Join(dir, "filler.txt"))
		if err != nil {
			return err
		}
		text = string(filler) + "\n\n" + text
	}
	line := fmt.Sprintf("%s event=%s source=%s mode=%s marker=%s bytes=%d copied_claude_md=%s cwd=%s "+
		"claude_project_dir=%s\n", stamp, in.Event, in.Source, mode, marker, len(text), copied, in.CWD,
		os.Getenv("CLAUDE_PROJECT_DIR"))
	if err := appendFile(filepath.Join(dir, "log.txt"), line); err != nil {
		return err
	}
	switch mode {
	case "json", "big":
		out, err := json.Marshal(map[string]any{"hookSpecificOutput": map[string]string{
			"hookEventName": in.Event, "additionalContext": text}})
		if err != nil {
			return err
		}
		fmt.Println(string(out))
	case "plain":
		fmt.Println(text)
	case "none":
	default:
		return fmt.Errorf("mode %q is not json, plain, big or none", mode)
	}
	return nil
}

// setting returns the trimmed contents of dir/name, or def when the file is missing or empty.
func setting(dir, name, def string) string {
	b, err := os.ReadFile(filepath.Join(dir, name))
	if s := strings.TrimSpace(string(b)); err == nil && s != "" {
		return s
	}
	return def
}

// appendFile appends line to file, creating it.
func appendFile(file, line string) error {
	f, err := os.OpenFile(file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(line); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
