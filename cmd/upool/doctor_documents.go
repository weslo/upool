package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/weslo/upool/internal/hooks"
	"github.com/weslo/upool/internal/tcc"
)

// checkDocumentsAccess is the check that has the desktop app's helper run upool's create hook, so
// that macOS asks whether upool may access the Documents folder while someone is there to answer,
// rather than under a session's start, where nobody sees it (#286).
const checkDocumentsAccess = "documents-access"

// documentsBound bounds the check's run of the hook. The hook's patience with its git call, 2 s,
// and its read of the TCC log, about a second, put its waiting line on stderr well within it, and
// an answered prompt lets git through at once (#286).
const documentsBound = 10 * time.Second

// helperPath is where the desktop app keeps the helper that runs its sessions' create hooks,
// relative to its bundle, Claude.app (docs/contracts.md (a), the "Process running the hook" row).
const helperPath = "Contents/Helpers/disclaimer"

// desktopHelper is the desktop app's helper as the documents-access check reaches it.
type desktopHelper struct {
	// paths are where the helper may be, in the order the check looks: desktopHelpers in run, and
	// none in a test that does not set it, where the check does not run.
	paths []string
	// run runs a command through the helper; nil is runHelper, as a process.
	run helperRunner
	// log is the unified log, which the check reads to tell a dry run the bound ended while it
	// waited from one that could not tell; nil is tcc.LogShow.
	log tcc.Log
}

// helperRunner runs argv through the helper at path, in dir, with env as its environment and stdin
// as its standard input, until ctx ends, and returns its stderr and its exit code. An error is for
// a helper that could not be run, or that ctx ended, which it returns.
type helperRunner func(ctx context.Context, helper string, argv, env []string, dir string,
	stdin []byte) (stderr []byte, exit int, err error)

// runHelper is the helperRunner that runs the helper as a process, in a process group of its own:
// once ctx ends, the whole group is killed, the hook and its git among it, so that a hook macOS
// holds outlives no doctor run. Its PWD is dir, so that Go's start-up in the hook finds its working
// directory without reading it, and the hook reaches its git call, where it can say it is waiting
// (evidence/consent-probe/startup.txt).
func runHelper(ctx context.Context, helper string, argv, env []string, dir string,
	stdin []byte) ([]byte, int, error) {
	cmd := exec.CommandContext(ctx, helper, argv...)
	// exec keeps the last of duplicate keys, so this PWD wins over the caller's.
	cmd.Dir, cmd.Env, cmd.Stdin = dir, append(slices.Clip(env), "PWD="+dir), bytes.NewReader(stdin)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	var exit *exec.ExitError
	switch {
	case ctx.Err() != nil:
		return stderr.Bytes(), -1, ctx.Err()
	case errors.As(err, &exit):
		return stderr.Bytes(), exit.ExitCode(), nil
	case err != nil:
		return stderr.Bytes(), -1, fmt.Errorf("running %s: %w", helper, err)
	}
	return stderr.Bytes(), 0, nil
}

// readable returns why the check cannot read the unified log, as for a standard account, or nil
// when it can, bounded as a dry run is.
func (h desktopHelper) readable(ctx context.Context, inv *invocation) error {
	l := h.log
	if l == nil {
		l = tcc.LogShow{Env: inv.environ()}
	}
	ctx, cancel := context.WithTimeout(ctx, documentsBound)
	defer cancel()
	return tcc.Readable(ctx, l)
}

// findHelper returns the first of paths that is an executable file.
func findHelper(paths []string) (string, bool) {
	for _, p := range paths {
		if info, err := os.Stat(p); err == nil && info.Mode().IsRegular() &&
			info.Mode().Perm()&0o111 != 0 {
			return p, true
		}
	}
	return "", false
}

// createHookUpool returns the upool that repo's .claude/settings.local.json registers as its
// create hook, as init writes it (A26), or "" for none. It is the binary the app runs, and the one
// macOS's prompt names, by its resolved path.
func createHookUpool(repo string) string {
	b, err := os.ReadFile(filepath.Join(repo, projectSettings))
	if err != nil {
		return "" // the hook-settings check reports why
	}
	registered, err := registeredHooks(b)
	if err != nil {
		return "" // as above
	}
	for _, c := range registered["WorktreeCreate"] {
		if c.Type == "command" && filepath.IsAbs(c.Command) &&
			filepath.Base(c.Command) == upoolName &&
			slices.Equal(c.Args, []string{"hook", "worktree-create"}) {
			return c.Command
		}
	}
	return ""
}

// probeRequest is the create request the check sends: a desktop session's, its transcript_path
// empty and with no prompt_id (docs/contracts.md (a), "Telling the kinds apart"), from repo, the
// fields testdata/captures/hooks/desktop-session-create.json has.
func probeRequest(repo string) ([]byte, error) {
	return json.Marshal(struct {
		SessionID      string `json:"session_id"`
		TranscriptPath string `json:"transcript_path"`
		Cwd            string `json:"cwd"`
		HookEventName  string `json:"hook_event_name"`
		Name           string `json:"name"`
	}{"upool-" + checkDocumentsAccess, "", repo, hooks.EventWorktreeCreate,
		"upool-" + checkDocumentsAccess})
}

// documentsAccessCheck has the desktop app's helper run upool, the binary the project's create
// hook runs, as `upool hook worktree-create --dry-run`, with a desktop session's create request
// for repo on stdin, bounded at documentsBound (#286). The hook makes its git call in repo and
// stops, taking no slot. The first time upool reads under ~/Documents from the helper, macOS asks
// whether it may, and the hook waits for the answer, saying so on stderr. The check passes once
// the hook answers; warns while the prompt is open, the bound having ended the hook, saying to
// answer it and run doctor again; fails, naming the setting, when macOS denies upool the folder,
// though the answer came within the bound, after the waiting line; and fails with the hook's error
// otherwise. Where the unified log cannot be read, as from a standard account, it cannot tell a
// prompt from a stall: a dry run the bound ended with no waiting line, or one that failed saying
// it could not read the log, warns that macOS may be asking, with what to do either way. It warns,
// naming where it looked, when the helper is not there, or when the project registers no create
// hook. With no helper to look for, off macOS, it reports false and runs nothing.
func documentsAccessCheck(ctx context.Context, inv *invocation, repo,
	upool string) (doctorCheck, bool) {
	h := inv.desktop
	if len(h.paths) == 0 {
		return doctorCheck{}, false
	}
	helper, found := findHelper(h.paths)
	if !found {
		return doctorCheck{checkDocumentsAccess, checkWarn, fmt.Sprintf("skipped: the Claude "+
			"desktop app's helper, which runs its sessions' create hooks, is not at %s",
			strings.Join(h.paths, " or "))}, true
	}
	if upool == "" {
		return doctorCheck{checkDocumentsAccess, checkWarn, fmt.Sprintf("skipped: %s registers "+
			"no upool create hook to run: run upool init", filepath.Join(repo,
			projectSettings))}, true
	}
	stdin, err := probeRequest(repo)
	if err != nil {
		return failedCheck(checkDocumentsAccess, err), true
	}
	run := h.run
	if run == nil {
		run = runHelper
	}
	bounded, cancel := context.WithTimeout(ctx, documentsBound)
	defer cancel()
	start := inv.now()
	stderr, exit, err := run(bounded, helper, []string{upool, "hook", "worktree-create",
		dryRunFlag}, inv.environ(), repo, stdin)
	took := inv.now().Sub(start)
	name := filepath.Base(upool)
	pending := fmt.Sprintf("macOS is asking whether %s may access Documents; answer it, then "+
		"run upool doctor again", name)
	// An unreadable log means the check cannot tell, not that no prompt is open (decision on
	// #286, 2026-11-04).
	unknown := fmt.Sprintf("macOS may be asking whether %s may access Documents, but upool "+
		"cannot read the unified log to tell, which needs an admin account: answer the prompt "+
		"if it is showing, then run upool doctor again; otherwise %s may be denied the "+
		"Documents folder in System Settings → Privacy & Security → Files and Folders, or its "+
		"git may be hung", name, name)
	// The waiting line tells something only of a hook the bound ended: one that wrote it and then
	// ended on its own was answered meanwhile, and its exit says how.
	waited := bytes.Contains(stderr, []byte("upool: "+hooks.WaitingLine))
	switch {
	case err == nil && exit == 0:
		return doctorCheck{checkDocumentsAccess, checkPass, fmt.Sprintf("the desktop app's "+
			"helper ran %s's create hook, and its git read %s in %d ms", name, repo,
			took.Milliseconds())}, true
	case errors.Is(err, context.DeadlineExceeded) && !waited:
		if readErr := h.readable(ctx, inv); readErr != nil {
			return doctorCheck{checkDocumentsAccess, checkWarn, unknown + fmt.Sprintf(" (%v)",
				readErr)}, true
		}
		return doctorCheck{checkDocumentsAccess, checkWarn, pending + fmt.Sprintf(" (the hook "+
			"did not answer within %s)", documentsBound)}, true
	case errors.Is(err, context.DeadlineExceeded):
		return doctorCheck{checkDocumentsAccess, checkWarn, pending}, true
	case err != nil:
		return failedCheck(checkDocumentsAccess, err), true
	case bytes.Contains(stderr, []byte("Operation not permitted")):
		return doctorCheck{checkDocumentsAccess, checkFail, fmt.Sprintf("macOS denies %s "+
			"access to Documents: allow it in System Settings → Privacy & Security → Files and "+
			"Folders, then run upool doctor again", name)}, true
	case bytes.Contains(stderr, []byte(hooks.LogUnreadable)):
		return doctorCheck{checkDocumentsAccess, checkWarn, unknown}, true
	}
	return doctorCheck{checkDocumentsAccess, checkFail, fmt.Sprintf("the create hook failed "+
		"under the desktop app's helper, exit %d: %s", exit, lastLine(stderr))}, true
}

// lastLine returns the last line of out that is not blank, without upool's prefix, or a note that
// there was none.
func lastLine(out []byte) string {
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if line := strings.TrimSpace(lines[len(lines)-1]); line != "" {
		return strings.TrimPrefix(line, "upool: ")
	}
	return "it wrote nothing on stderr"
}
