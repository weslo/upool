// Command upool keeps a per-project pool of pre-warmed Unity worktrees and leases them to agents.
package main

import (
	"errors"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/weslo/upool/internal/output"
)

// version is set at build time with -ldflags "-X main.version=<version>".
var version = "dev"

// commandTable is upool's commands by name. Each arrives with its own issue, so usage lists only
// those that work (docs/design.md, "One binary, a dozen commands, one JSON file per pool").
var commandTable = map[string]command{
	"doctor":  doctorCommand,
	"init":    initCommand,
	"path":    pathCommand,
	"release": releaseCommand,
	"run":     runCommand,
	"setup":   setupCommand,
	"status":  statusCommand,
	"take":    takeCommand,
	"which":   whichCommand,
}

// hookTable is the events `upool hook <event>` handles, one per hook script that `upool setup`
// installs; failExit follows docs/contracts.md (d). worktree-create refuses a session in a pool
// until #78; the others are stubs until their M2 issues.
var hookTable = map[string]hook{
	"worktree-create": {handle: worktreeCreate, failExit: output.ExitFailure},
	"worktree-remove": {handle: notImplemented, failExit: output.ExitOK},
	"session-start":   {handle: notImplemented, failExit: output.ExitOK},
}

// errNotImplemented is what the stub hook handlers return.
var errNotImplemented = errors.New("not implemented")

func notImplemented(*invocation) (string, error) { return "", errNotImplemented }

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, os.Getenv))
}

// run is upool with its process boundary passed in, so tests call it in-process. It returns the
// exit code. The rest of the boundary, the clock, the working directory, the environment's full
// list, the process IDs, the binary's path and the delivery of SIGINT and SIGTERM, it takes from
// the process; a test of a command that reads them builds a dispatcher with its own.
func run(args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) int {
	d := dispatcher{
		commands:   commandTable,
		hooks:      hookTable,
		stdin:      stdin,
		stdout:     stdout,
		stderr:     stderr,
		getenv:     getenv,
		environ:    os.Environ,
		getwd:      os.Getwd,
		now:        time.Now,
		pid:        os.Getpid(),
		ppid:       os.Getppid(),
		executable: os.Executable,
		notify: func(c chan<- os.Signal) {
			signal.Notify(c, syscall.SIGINT, syscall.SIGTERM)
		},
		stopNotify: func(c chan<- os.Signal) { signal.Stop(c) },
	}
	return d.run(args)
}
