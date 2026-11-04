package hooks

import (
	"context"
	"fmt"
	"time"

	"github.com/weslo/upool/internal/git"
	"github.com/weslo/upool/internal/tcc"
)

// gitPatience is how long the create hook's git call in the main checkout may run before the hook
// asks why: git answers in tens of milliseconds, but under the desktop app's helper it blocks for
// as long as macOS's prompt asking whether upool may access the Documents folder stays unanswered,
// which in the benchmark of 2026-11-03 was 45 minutes (#286;
// testdata/captures/tcc/documents-prompting.ndjson).
const gitPatience = 2 * time.Second

// consentWait is how long, from the start of its git call, the hook waits for a person to answer
// that prompt: the desktop app cancels a session start that has not started within 300 s
// (docs/evidence/logs/desktop-consent-stall-main.log), less the 5 s A11 leaves, so that the hook
// fails with upool's message before the app gives up.
const consentWait = 300*time.Second - 5*time.Second

// heartbeat is how often the hook says again that it is waiting for the prompt.
const heartbeat = 5 * time.Second

// stopWait is how long the hook waits for a git call it has cancelled to end: a process macOS holds
// may not end at once, and the hook's answer matters more than its child's exit.
const stopWait = time.Second

// WaitingLine is how the line the hook writes while the prompt holds its git call begins, after
// "upool: ", for `upool doctor` to find on the hook's stderr.
const WaitingLine = "waiting: macOS is asking whether "

// Waiting is the line the hook writes on stderr, after "upool: ", every heartbeat while macOS's
// prompt about name, the name the prompt gives the hook's binary, holds its git call (#286).
func Waiting(name string) string {
	return WaitingLine + name + " may access Documents; answer the prompt"
}

// listed is what a git worktree list returned.
type listed struct {
	worktrees []git.Worktree
	err       error
}

// worktreeList is the pooled branch's git call in the main checkout, `git worktree list`, the
// first one that reads under it, with patience (#286). It returns git's answer if git gives one
// within gitPatience. Otherwise it reads env.Consent, the unified log, for a TCC prompt about
// env.Self that is still open: with one, it waits for git until consentWait, writing Waiting on
// stderr every heartbeat, and git's answer ends the wait however the person answers; with none, it
// ends git and fails at once, naming the folder and the likely causes, so that the app's
// `WorktreeCreate hook failed` line says why.
func worktreeList(ctx context.Context, env Env, checkout string) ([]git.Worktree, error) {
	start := time.Now()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan listed, 1)
	go func() {
		wts, err := env.Git.WorktreeList(ctx, checkout)
		done <- listed{wts, err}
	}()
	patience := time.NewTimer(gitPatience)
	defer patience.Stop()
	select {
	case r := <-done:
		return r.worktrees, r.err
	case <-patience.C:
	}
	type looked struct {
		prompt  tcc.Prompt
		pending bool
		err     error
	}
	found := make(chan looked, 1)
	go func() {
		p, pending, err := tcc.Pending(ctx, env.Consent, env.Self)
		found <- looked{p, pending, err}
	}()
	var l looked
	select {
	case r := <-done:
		return r.worktrees, r.err
	case l = <-found:
	}
	if !l.pending {
		stop(cancel, done)
		return nil, stalled(checkout, l.err)
	}
	waiting := "upool: " + Waiting(l.prompt.Name()) + "\n"
	// stderr is for warnings only; the app takes the worktree path from stdout.
	_, _ = fmt.Fprint(env.Stderr, waiting)
	deadline := time.NewTimer(time.Until(start.Add(consentWait)))
	defer deadline.Stop()
	tick := time.NewTicker(heartbeat)
	defer tick.Stop()
	for {
		select {
		case r := <-done:
			return r.worktrees, r.err
		case <-tick.C:
			_, _ = fmt.Fprint(env.Stderr, waiting) // as above
		case <-deadline.C:
			stop(cancel, done)
			return nil, fmt.Errorf("macOS asked whether %s may access Documents, and the prompt "+
				"was not answered within %s, the most the desktop app waits for a session to "+
				"start: answer it, then start the session again", l.prompt.Name(), consentWait)
		}
	}
}

// stop cancels a git call and waits at most stopWait for it to end.
func stop(cancel context.CancelFunc, done <-chan listed) {
	cancel()
	select {
	case <-done:
	case <-time.After(stopWait):
	}
}

// stalled is the error for a git call in checkout that did not answer within gitPatience while
// macOS showed no prompt for it, or while its log of prompts, whose reading failed with logErr,
// could not tell.
func stalled(checkout string, logErr error) error {
	why := "and macOS is not asking whether upool may access Documents"
	if logErr != nil {
		why = fmt.Sprintf("and macOS's log of its prompts could not be read (%v)", logErr)
	}
	return fmt.Errorf("git -C %s worktree list did not answer within %s, %s: the likely causes "+
		"are upool denied the Documents folder in System Settings → Privacy & Security → Files "+
		"and Folders, a prompt for it left unanswered for over %.0f minutes, or a hung git",
		checkout, gitPatience, why, tcc.Window.Minutes())
}
