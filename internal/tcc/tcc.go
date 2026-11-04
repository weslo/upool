// Package tcc reads what TCC, macOS's privacy framework, logs about the consent prompts it shows,
// so that the create hook can tell a git call macOS holds until someone answers its Documents
// prompt from one that is stuck (#286; docs/contracts.md (a), the "Process running the hook" row).
//
// The desktop app runs the create hook disclaimed, so TCC asks about upool itself, by its resolved
// binary path, and not about the app: a Homebrew upool is asked about as
// /opt/homebrew/Cellar/upool/<version>/bin/upool. tccd logs the prompt as AUTHREQ_PROMPTING, with
// the request's msgID, and its answer as AUTHREQ_RESULT with the same msgID
// (testdata/captures/tcc/). A request made while that prompt is still open logs no prompt of its
// own, so a prompt is looked for over Window, which is longer than the app waits for a session to
// start.
package tcc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// Documents is the TCC service of the Documents folder, which the desktop app's helper may not read
// without a person's consent (docs/contracts.md (a); A24).
const Documents = "kTCCServiceSystemPolicyDocumentsFolder"

// Window is how far back Pending looks for a prompt. A request made while a prompt for the same
// client is open waits on it and logs no prompt of its own: in the benchmark of 2026-11-03 a hook
// run through the helper 16 minutes after the first one's prompt logged none (#286). An hour of
// the log took about a second to read on the benchmark's Mac.
const Window = time.Hour

// logShow is the unified log's command-line tool, at the path every macOS has it at: the helper's
// PATH is not the user's (docs/design.md, "Repository, CI and Homebrew distribution").
const logShow = "/usr/bin/log"

// Log reads the unified log: Show returns what `log show --last <window> --style ndjson
// --predicate <predicate>` prints, one JSON object per line. Tests replace it with captures.
type Log interface {
	Show(ctx context.Context, window time.Duration, predicate string) ([]byte, error)
}

// LogShow is the Log that runs /usr/bin/log, with Env as its whole environment.
type LogShow struct {
	Env []string
}

var _ Log = LogShow{}

// Show runs `log show`. It runs in /, so that a working directory under a protected folder cannot
// stop it. An admin user reads the log without sudo, and so does the desktop app's helper
// (#286).
func (l LogShow) Show(ctx context.Context, window time.Duration, predicate string) ([]byte,
	error) {
	cmd := exec.CommandContext(ctx, logShow, "show", "--last",
		fmt.Sprintf("%dm", int(window/time.Minute)), "--style", "ndjson", "--predicate", predicate)
	cmd.Dir, cmd.Env = "/", l.Env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			err = fmt.Errorf("%w: %s", err, msg)
		}
		return nil, fmt.Errorf("reading the TCC log with %s show: %w", logShow, err)
	}
	return out, nil
}

// Prompt is a consent prompt TCC showed: the request's msgID, the service it asked for, and its
// subject, the client it asked about: a bundle identifier, or for a binary with none, such as
// upool, the binary's path.
type Prompt struct {
	MsgID   string
	Service string
	Subject string
}

// Name is how a person knows the prompt's subject: the last element of a binary's path, which is
// how the prompt names it ("upool"), or else the subject as it is.
func (p Prompt) Name() string {
	if i := strings.LastIndexByte(p.Subject, '/'); i >= 0 && i < len(p.Subject)-1 {
		return p.Subject[i+1:]
	}
	return p.Subject
}

// promptsPredicate selects the prompts for the Documents folder
// (testdata/captures/tcc/documents-prompting.ndjson).
var promptsPredicate = `subsystem == "com.apple.TCC" AND eventMessage BEGINSWITH ` +
	`"AUTHREQ_PROMPTING: " AND eventMessage CONTAINS "service=` + Documents + `,"`

// resultsPredicate selects the results of the requests ids name
// (testdata/captures/tcc/documents-result-*.ndjson). Each id is a msgID ParsePrompts matched, so
// it holds only digits and a dot.
func resultsPredicate(ids []string) string {
	terms := make([]string, len(ids))
	for i, id := range ids {
		terms[i] = `eventMessage BEGINSWITH "AUTHREQ_RESULT: msgID=` + id + `,"`
	}
	return `subsystem == "com.apple.TCC" AND (` + strings.Join(terms, " OR ") + `)`
}

// The messages tccd logs, as the captures show them: a prompt, with its msgID, service and
// subject, and a request's result, with its msgID.
var (
	promptingLine = regexp.MustCompile(`^AUTHREQ_PROMPTING: msgID=([0-9]+\.[0-9]+), ` +
		`service=([A-Za-z]+), subject=Sub:\{([^}]*)\}`)
	resultLine = regexp.MustCompile(`^AUTHREQ_RESULT: msgID=([0-9]+\.[0-9]+),`)
)

// messages returns the eventMessage of each event in out, the ndjson `log show` prints, in order.
// The line that ends it, which counts the events, has none.
func messages(out []byte) ([]string, error) {
	var msgs []string
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var event struct {
			EventMessage *string `json:"eventMessage"`
		}
		if err := json.Unmarshal(line, &event); err != nil {
			return nil, fmt.Errorf("reading the TCC log's line %q: %w", line, err)
		}
		if event.EventMessage != nil {
			msgs = append(msgs, *event.EventMessage)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("reading the TCC log: %w", err)
	}
	return msgs, nil
}

// ParsePrompts returns the prompts in out, the ndjson `log show` prints, in the order tccd logged
// them.
func ParsePrompts(out []byte) ([]Prompt, error) {
	msgs, err := messages(out)
	if err != nil {
		return nil, err
	}
	var prompts []Prompt
	for _, m := range msgs {
		if g := promptingLine.FindStringSubmatch(m); g != nil {
			prompts = append(prompts, Prompt{MsgID: g[1], Service: g[2], Subject: g[3]})
		}
	}
	return prompts, nil
}

// ParseResults returns the msgIDs of the requests out, the ndjson `log show` prints, records a
// result for: those whose prompt has been answered.
func ParseResults(out []byte) (map[string]bool, error) {
	msgs, err := messages(out)
	if err != nil {
		return nil, err
	}
	answered := map[string]bool{}
	for _, m := range msgs {
		if g := resultLine.FindStringSubmatch(m); g != nil {
			answered[g[1]] = true
		}
	}
	return answered, nil
}

// Pending returns the latest prompt for the Documents folder logged within Window that has no
// result, and whether there is one: a prompt still on the screen. Only a prompt about subject
// counts, the hook's own binary with its symlinks resolved, as TCC names it; an empty subject
// counts every one.
func Pending(ctx context.Context, l Log, subject string) (Prompt, bool, error) {
	if l == nil {
		return Prompt{}, false, errors.New("no TCC log to read")
	}
	out, err := l.Show(ctx, Window, promptsPredicate)
	if err != nil {
		return Prompt{}, false, err
	}
	all, err := ParsePrompts(out)
	if err != nil {
		return Prompt{}, false, err
	}
	var prompts []Prompt
	var ids []string
	for _, p := range all {
		if p.Service == Documents && (subject == "" || p.Subject == subject) {
			prompts, ids = append(prompts, p), append(ids, p.MsgID)
		}
	}
	if len(prompts) == 0 {
		return Prompt{}, false, nil
	}
	out, err = l.Show(ctx, Window, resultsPredicate(ids))
	if err != nil {
		return Prompt{}, false, err
	}
	answered, err := ParseResults(out)
	if err != nil {
		return Prompt{}, false, err
	}
	for i := len(prompts) - 1; i >= 0; i-- {
		if !answered[prompts[i].MsgID] {
			return prompts[i], true, nil
		}
	}
	return Prompt{}, false, nil
}
