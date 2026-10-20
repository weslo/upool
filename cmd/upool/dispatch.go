package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/weslo/upool/internal/output"
)

// invocation is what a handler gets: one call's arguments and parsed flags, and the process
// boundary run was given, less stdout. Only the dispatcher writes stdout (docs/design.md,
// "Repository, CI and Homebrew distribution", the rules after the layout), so a handler returns
// its result rather than printing it.
type invocation struct {
	// args are the positional arguments, without the flags or a "--".
	args []string
	// flags is the command's parsed flag set, through which a handler can Visit the flags the
	// command line set, the first configuration layer (internal/config). It is nil for a hook.
	flags *flag.FlagSet
	// stdin is upool's standard input, which carries a hook's JSON.
	stdin io.Reader
	// stderr is for progress, such as a wait for a slot; the result, its warnings and its error
	// go back to the dispatcher.
	stderr io.Writer
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
	// executable returns the path of upool's own binary, which `upool setup` looks for on PATH.
	executable func() (string, error)
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

// hookHandler runs `upool hook <event>` and returns the worktree path the dispatcher prints, and
// an error (docs/design.md, "Repository, CI and Homebrew distribution", internal/hooks).
type hookHandler func(inv *invocation) (path string, err error)

// hook is one row of the hook table.
type hook struct {
	handle hookHandler
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
	executable func() (string, error)
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
	positional, err := parseFlags(fs, args)
	if errors.Is(err, flag.ErrHelp) {
		return d.help(cmd, fs)
	}
	if err != nil {
		err = fmt.Errorf("%w; usage: upool %s", err, cmd.usage)
		r := output.Result{Command: name, Err: err}
		return output.Render(d.stdout, d.stderr, jsonRequested(args), r)
	}
	data, warnings, err := handle(d.invocation(positional, fs))
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

// hook runs `upool hook <event>`. On success stdout gets the handler's path and a newline and
// nothing else, because the app takes stdout as the worktree path (docs/contracts.md (a),
// "WorktreeCreate output and app behaviour"). On failure the error goes to stderr and upool exits
// with the event's failExit.
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
	path, err := h.handle(d.invocation(nil, nil))
	if err != nil {
		say(d.stderr, fmt.Sprintf("hook %s: %v", event, err))
		return h.failExit
	}
	if _, err := fmt.Fprintln(d.stdout, path); err != nil {
		say(d.stderr, fmt.Sprintf("hook %s: writing the path: %v", event, err))
		return h.failExit
	}
	return output.ExitOK
}

// invocation returns what a handler gets for args and fs.
func (d *dispatcher) invocation(args []string, fs *flag.FlagSet) *invocation {
	return &invocation{
		args:       args,
		flags:      fs,
		stdin:      d.stdin,
		stderr:     d.stderr,
		getenv:     d.getenv,
		environ:    d.environ,
		getwd:      d.getwd,
		home:       d.getenv("HOME"),
		now:        d.now,
		pid:        d.pid,
		executable: d.executable,
	}
}

// parseFlags parses args with fs and returns the positional arguments in order. It accepts flags
// before, between and after them, as the design's usage lines place them (`upool take x --json`),
// where the flag package alone stops at the first positional argument. A "--" ends the flags, and
// everything after it is positional, so `upool run <name> -- <cmd...>` leaves the command's own
// flags alone.
func parseFlags(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return positional, nil
		}
		// Parse stops either at a positional argument, which it leaves first in rest, or just
		// after a "--", which it consumes.
		if parsed := len(args) - len(rest); parsed > 0 && args[parsed-1] == "--" {
			return append(positional, rest...), nil
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
// commands in the table, the implemented ones, and not `upool hook`, which only the hook scripts
// that `upool setup` installs call (docs/design.md, "Repository, CI and Homebrew distribution").
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
