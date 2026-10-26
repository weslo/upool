package hooks

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/weslo/upool/internal/pool"
)

// SourceStartup is a SessionStart request's source when a session starts. A session that resumes,
// clears or compacts sends resume, clear or compact instead (docs/contracts.md (b), "SessionStart
// sources").
const SourceStartup = "startup"

// projectPathVar is the variable the session-start hook exports for a session in a slot, the one
// the Unity CLI targets by (docs/contracts.md (b) and (c)).
const projectPathVar = "UNITY_PROJECT_PATH"

// SessionStart handles a SessionStart request. For a session in a slot, found by its session_id
// or, failing that, by the slot that contains its stdin cwd (sessionSlot; A46), it confirms the
// slot's lease in-process on startup, as `upool take --confirm` does, recording the session_id
// for its later starts, and then starts the detached top-up through env.StartRefresh (#79). On
// any other source it only records env.ParentPID as the lease's holder while the slot has one,
// re-copying no agent file, running no reset and starting no top-up (A14). Then, whatever the
// source and whatever the confirm did, it exports UNITY_PROJECT_PATH through env.EnvFile, for a
// resume found by its session_id with cwd outside the slot too: a `claude -p --resume` launched
// from the main checkout reports that checkout as its cwd, but works in the slot (A47). The stdin
// cwd is read and $PWD never is, since the hook's own working directory is the main checkout
// ((b)). For a session in a slot it returns the context document that gives the session upool's
// instructions (ContextDocument; A43), on every source, and even when the confirm fails, with that
// error. A session in no slot, the main checkout's own among them, is left alone and gets
// nothing. The hook exits 0 whatever this returns, its error on stderr:
// SessionStart has no abort role ((d)).
func SessionStart(ctx context.Context, env Env, req Request) (string, error) {
	if req.HookEventName != EventSessionStart {
		return "", fmt.Errorf("stdin is a %q request, not %s", req.HookEventName,
			EventSessionStart)
	}
	if !filepath.IsAbs(req.Cwd) {
		return "", fmt.Errorf("the request's cwd %q is not an absolute path", req.Cwd)
	}
	ref, ok, err := sessionSlot(env.Home, req.SessionID, req.Cwd)
	if err != nil || !ok {
		return "", err
	}
	doc, err := ContextDocument(EventSessionStart)
	errs := []error{err}
	if req.Source == SourceStartup {
		c, warnings, err := Confirm(ctx, env, ref.slot.Path, req.SessionID)
		for _, w := range warnings {
			// stderr is for warnings only; stdout would reach the session's context.
			_, _ = fmt.Fprintf(env.Stderr, "upool: %s\n", w)
		}
		if err != nil {
			errs = append(errs, err)
		} else if env.StartRefresh != nil {
			if err := env.StartRefresh(c.Repo, ref.repo.Key); err != nil {
				errs = append(errs, fmt.Errorf("starting the top-up: %w", err))
			}
		}
	} else if err := recordHolder(ctx, env, ref); err != nil {
		errs = append(errs, err)
	}
	if err := exportProjectPath(env.EnvFile, ref.slot.Path); err != nil {
		errs = append(errs, err)
	}
	return doc, errors.Join(errs...)
}

// recordHolder records env.ParentPID as the holder of the lease on ref's slot, if it has one: a
// resumed session may run in a new CLI process, and the lease must follow that one's life from
// then on (A14; A6). A lease that ended or changed since ref was read is no longer the session's,
// and is left alone.
func recordHolder(ctx context.Context, env Env, ref slotRef) error {
	s := ref.slot
	if s.Lease == nil {
		return nil
	}
	pid, start, err := holder(env.ParentPID)
	if err != nil || pid == nil {
		return err
	}
	return pool.Update(ctx, env.Home, ref.repo.Key, func(p *pool.Pool) error {
		i := slices.IndexFunc(p.Slots, func(cur pool.Slot) bool { return cur.Name == s.Name })
		if i < 0 || p.Slots[i].Lease == nil || p.Slots[i].Lease.LeaseID != s.Lease.LeaseID {
			return nil
		}
		p.Slots[i].Lease.HolderPID, p.Slots[i].Lease.HolderPIDStart = pid, start
		return nil
	})
}

// exportProjectPath appends `export UNITY_PROJECT_PATH=<slot>`, the path quoted for the shell, to
// file, the one CLAUDE_ENV_FILE names, whose lines reach the session's Bash tool
// (docs/contracts.md (b), `CLAUDE_ENV_FILE`). A file that already holds the line is left as it
// is, so a hook run again for the same session, as compact runs it, exports the slot once.
func exportProjectPath(file, slot string) error {
	if file == "" {
		return fmt.Errorf("CLAUDE_ENV_FILE is not set, so %s was not exported", projectPathVar)
	}
	line := "export " + projectPathVar + "=" + shellQuote(slot)
	old, err := os.ReadFile(file)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("reading %s: %w", file, err)
	}
	if slices.Contains(strings.Split(string(old), "\n"), line) {
		return nil
	}
	if len(old) > 0 && !bytes.HasSuffix(old, []byte("\n")) {
		line = "\n" + line
	}
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("exporting %s: %w", projectPathVar, err)
	}
	if _, err := f.WriteString(line + "\n"); err != nil {
		// The write failed, which is the error to report; a failed close adds nothing to it.
		_ = f.Close()
		return fmt.Errorf("writing %s: %w", file, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", file, err)
	}
	return nil
}

// shellQuote returns s quoted for sh: in single quotes, each single quote in it closed, escaped
// and reopened.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
