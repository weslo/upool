package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/weslo/upool/internal/hooks"
	"github.com/weslo/upool/internal/output"
	"github.com/weslo/upool/internal/proc"
)

// invocation is what a handler gets: one call's arguments and parsed flags, and the process
// boundary run was given, less stdout. Only the dispatcher writes stdout (docs/design.md,
// "Repository, CI and Homebrew distribution", the rules after the layout), so a handler returns
// its result rather than printing it.
type invocation struct {
	// args are the positional arguments, without the flags or a "--".
	args []string
	// dash is the index in args of the first argument after a "--" that ended the flags, and -1
	// when the command line had none, so that `upool run` can tell its own arguments from its
	// command's.
	dash int
	// flags is the command's parsed flag set, through which a handler can Visit the flags the
	// command line set, the first configuration layer (internal/config). It is nil for a hook.
	flags *flag.FlagSet
	// stdin is upool's standard input, which carries a hook's JSON.
	stdin io.Reader
	// stderr is for progress, such as a wait for a slot; the result, its warnings and its error
	// go back to the dispatcher.
	stderr io.Writer
	// stdout is upool's standard output, for a command upool runs and nothing else: `upool run`
	// hands it to its command, so that an agent sees the terminal. upool's own output goes back
	// to the dispatcher, which is the only part of upool that writes it.
	stdout io.Writer
	// notify starts delivering SIGINT and SIGTERM to c instead of letting them end upool, and
	// stopNotify stops it, as signal.Notify and signal.Stop do; `upool run` forwards them to its
	// command. Both are set or both nil, as where nothing delivers signals, in most tests.
	notify, stopNotify func(c chan<- os.Signal)
	// getenv looks up an environment variable.
	getenv func(string) string
	// environ returns the whole environment as "KEY=value" entries, the environment a handler
	// gives the git it runs (internal/git), which getenv cannot list.
	environ func() []string
	// getwd returns the working directory, from which `upool init` finds its repository.
	getwd func() (string, error)
	// home is $HOME as getenv reports it, "" when unset; internal/config and internal/statefile
	// refuse a home directory that is not absolute.
	home string
	// now is the clock, so that tests of timeouts and ages need no sleeps.
	now func() time.Time
	// pid is upool's own process ID, which a clone records as its slot's owner_pid
	// (docs/contracts.md (e) State schemas; A4).
	pid int
	// ppid is upool's parent's process ID: for a hook, the Claude Code process that ran it, which
	// a subagent's worktree records as its parent_pid ((e), Agent worktree).
	ppid int
	// executable returns the path of upool's own binary, which `upool init` looks for on PATH.
	executable func() (string, error)
	// procs tells whether processes are alive; nil is the operating system, proc.System. Tests of
	// a command that watches a process it did not start, such as the Editor `upool open`
	// launches, inject their own.
	procs proc.Checker
}

// checker is inv.procs, or proc.System when it is nil.
func (inv *invocation) checker() proc.Checker {
	if inv.procs == nil {
		return proc.System{}
	}
	return inv.procs
}

// handler runs a command and returns its data, its warnings and its error, which output.Render
// prints.
type handler func(inv *invocation) (data any, warnings []string, err error)

// command is one row of the command table.
type command struct {
	// usage is the command's usage line after "upool ", such as "which [--json]".
	usage string
	// setup defines the command's flags on fs, other than --json, which every command has, and
	// returns its handler, which reads their values once the dispatcher has parsed fs.
	setup func(fs *flag.FlagSet) handler
}

// hookHandler runs `upool hook <event>` and returns what the dispatcher prints, and an error: the
// worktree path for WorktreeCreate, and for SessionStart and SubagentStart the context document
// that gives the session upool's instructions (docs/design.md, "Repository, CI and Homebrew
// distribution", internal/hooks; A43).
type hookHandler func(inv *invocation) (path string, err error)

// hookDecider runs a hook that answers with a decision rather than a path, PreToolUse's lane guard,
// and returns the decision for the dispatcher to print (internal/hooks.Decision.Output).
type hookDecider func(inv *invocation) (hooks.Decision, error)

// hook is one row of the hook table: handle, or for a hook that decides, decide.
type hook struct {
	handle hookHandler
	decide hookDecider
	// failExit is the exit code when handle fails. Claude Code's hook contract sets it, not
	// upool's own exit codes: only worktree-create exits non-zero, which aborts the session, and
	// worktree-remove always exits 0 (docs/contracts.md (d), "upool's own exit codes").
	failExit int
}

// dispatcher is upool's command line: the tables it routes on, and the process boundary it hands
// each handler. It is the only writer of stdout.
type dispatcher struct {
	commands   map[string]command
	hooks      map[string]hook
	stdin      io.Reader
	stdout     io.Writer
	stderr     io.Writer
	getenv     func(string) string
	environ    func() []string
	getwd      func() (string, error)
	now        func() time.Time
	pid        int
	ppid       int
	executable func() (string, error)
	// notify and stopNotify are as in invocation.
	notify, stopNotify func(c chan<- os.Signal)
	// procs is as in invocation.
	procs proc.Checker
}

// run dispatches args, the command line after the program name, and returns the exit code.
func (d *dispatcher) run(args []string) int {
	if len(args) == 1 && args[0] == "--version" {
		if _, err := fmt.Fprintln(d.stdout, "upool", version); err != nil {
			return output.ExitFailure
		}
		return output.ExitOK
	}
	if len(args) > 0 && args[0] == "hook" {
		return d.hook(args[1:])
	}
	if len(args) > 0 {
		if cmd, ok := d.commands[args[0]]; ok {
			return d.command(args[0], cmd, args[1:])
		}
	}
	// Nowhere is left to report a failure to write stderr.
	_, _ = io.WriteString(d.stderr, usage(d.commands))
	return output.ExitFailure
}

// command runs one command: it parses the command's flags wherever they sit among its arguments,
// calls its handler, and prints the result with output.Render, as an envelope under --json.
func (d *dispatcher) command(name string, cmd command, args []string) int {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard) // a bad flag is reported below, in the envelope under --json
	asJSON := fs.Bool("json", false, "print the result as a JSON envelope")
	handle := cmd.setup(fs)
	positional, dash, err := parseFlags(fs, args)
	if errors.Is(err, flag.ErrHelp) {
		return d.help(cmd, fs)
	}
	if err != nil {
		err = fmt.Errorf("%w; usage: upool %s", err, cmd.usage)
		r := output.Result{Command: name, Err: err}
		return output.Render(d.stdout, d.stderr, jsonRequested(args), r)
	}
	inv := d.invocation(positional, fs)
	inv.dash = dash
	data, warnings, err := handle(inv)
	return output.Render(d.stdout, d.stderr, *asJSON, output.Result{
		Command:  name,
		Data:     data,
		Warnings: warnings,
		Err:      err,
	})
}

// help prints a command's usage line and its flags to stdout for -h or --help, and exits 0: a
// flag's description is where a command says what a person needs before using it, such as init's
// note on slots beyond ready (A21).
func (d *dispatcher) help(cmd command, fs *flag.FlagSet) int {
	var b strings.Builder
	b.WriteString("usage: upool " + cmd.usage + "\n\nflags:\n")
	fs.SetOutput(&b)
	fs.PrintDefaults()
	if _, err := io.WriteString(d.stdout, b.String()); err != nil {
		say(d.stderr, "writing the help: "+err.Error())
		return output.ExitFailure
	}
	return output.ExitOK
}

// hook runs `upool hook <event>`. On success stdout gets what the handler returns and a newline and
// nothing else: for WorktreeCreate the worktree path, which the app takes stdout as
// (docs/contracts.md (a), "WorktreeCreate output and app behaviour"), and for SessionStart and
// SubagentStart the context document, which Claude Code adds to the session's context (A43). A
// handler that returns nothing, as session-start does for a session in no slot, leaves stdout
// empty. On failure the error goes to stderr and upool exits with the event's failExit.
func (d *dispatcher) hook(args []string) int {
	events := strings.Join(slices.Sorted(maps.Keys(d.hooks)), ", ")
	if len(args) == 0 {
		say(d.stderr, "hook: missing event; want one of "+events)
		return output.ExitFailure
	}
	event := args[0]
	h, ok := d.hooks[event]
	if !ok {
		say(d.stderr, fmt.Sprintf("hook: unknown event %q; want one of %s", event, events))
		return output.ExitFailure
	}
	if len(args) > 1 {
		say(d.stderr, fmt.Sprintf("hook %s: unexpected arguments %q", event, args[1:]))
		return h.failExit
	}
	if h.decide != nil {
		return d.decision(event, h)
	}
	path, err := h.handle(d.invocation(nil, nil))
	if err != nil {
		say(d.stderr, fmt.Sprintf("hook %s: %v", event, err))
		return h.failExit
	}
	if path == "" {
		return output.ExitOK
	}
	if _, err := fmt.Fprintln(d.stdout, path); err != nil {
		say(d.stderr, fmt.Sprintf("hook %s: writing the path: %v", event, err))
		return h.failExit
	}
	return output.ExitOK
}

// decision runs the deciding hook h for event and prints its decision, a JSON document on one line,
// or nothing when the hook lets the call through (docs/design.md, "Lanes: scripts in parallel,
// scenes one at a time, `.meta` never regenerated"). On failure the error goes to stderr and upool
// exits with failExit, which the hook's registration turns into a denial (`"onFailure": "block"`).
func (d *dispatcher) decision(event string, h hook) int {
	dec, err := h.decide(d.invocation(nil, nil))
	if err != nil {
		say(d.stderr, fmt.Sprintf("hook %s: %v", event, err))
		return h.failExit
	}
	out, err := dec.Output()
	if err != nil {
		say(d.stderr, fmt.Sprintf("hook %s: encoding the decision: %v", event, err))
		return h.failExit
	}
	if out == nil {
		return output.ExitOK
	}
	if _, err := fmt.Fprintln(d.stdout, string(out)); err != nil {
		say(d.stderr, fmt.Sprintf("hook %s: writing the decision: %v", event, err))
		return h.failExit
	}
	return output.ExitOK
}

// invocation returns what a handler gets for args and fs.
func (d *dispatcher) invocation(args []string, fs *flag.FlagSet) *invocation {
	return &invocation{
		args:       args,
		dash:       -1,
		flags:      fs,
		stdin:      d.stdin,
		stderr:     d.stderr,
		stdout:     d.stdout,
		notify:     d.notify,
		stopNotify: d.stopNotify,
		getenv:     d.getenv,
		environ:    d.environ,
		getwd:      d.getwd,
		home:       d.getenv("HOME"),
		now:        d.now,
		pid:        d.pid,
		ppid:       d.ppid,
		executable: d.executable,
		procs:      d.procs,
	}
}

// parseFlags parses args with fs and returns the positional arguments in order. It accepts flags
// before, between and after them, as the design's usage lines place them (`upool take x --json`),
// where the flag package alone stops at the first positional argument. A "--" ends the flags, and
// everything after it is positional, so `upool run <name> -- <cmd...>` leaves the command's own
// flags alone; dash is the index in positional of the first argument after it, and -1 without
// one.
func parseFlags(fs *flag.FlagSet, args []string) (positional []string, dash int, err error) {
	for {
		if err := fs.Parse(args); err != nil {
			return nil, -1, err
		}
		rest := fs.Args()
		// Parse stops either at a positional argument, which it leaves first in rest, or just
		// after a "--", which it consumes.
		if parsed := len(args) - len(rest); parsed > 0 && args[parsed-1] == "--" {
			return append(positional, rest...), len(positional), nil
		}
		if len(rest) == 0 {
			return positional, -1, nil
		}
		positional = append(positional, rest[0])
		args = rest[1:]
	}
}

// jsonRequested reports whether args set --json before any "--", the last setting winning as in
// the flag package. A bad flag stops parsing before --json may have been reached, and the error
// is still owed as an envelope.
func jsonRequested(args []string) bool {
	asJSON := false
	for _, arg := range args {
		if arg == "--" {
			break
		}
		name, value, hasValue := strings.Cut(arg, "=")
		if name != "-json" && name != "--json" {
			continue
		}
		set, err := strconv.ParseBool(value)
		asJSON = !hasValue || (err == nil && set)
	}
	return asJSON
}

// usage is what upool prints on stderr for a command line it cannot dispatch. It lists only the
// commands in the table, the implemented ones, and not `upool hook`, which only the hooks that
// `upool init` registers run (docs/design.md, "Repository, CI and Homebrew distribution").
func usage(commands map[string]command) string {
	var b strings.Builder
	b.WriteString("usage: upool --version\n")
	for _, name := range slices.Sorted(maps.Keys(commands)) {
		b.WriteString("       upool " + commands[name].usage + "\n")
	}
	if len(commands) == 0 {
		b.WriteString("\nNo commands are implemented yet; docs/design.md lists the planned ones.\n")
	}
	return b.String()
}

// say writes one line to stderr after "upool: ", as output.Render does.
func say(stderr io.Writer, line string) {
	_, _ = fmt.Fprintf(stderr, "upool: %s\n", line) // nowhere left to report a failure to write stderr
}
