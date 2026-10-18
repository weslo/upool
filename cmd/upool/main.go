// Command upool keeps a per-project pool of pre-warmed Unity worktrees and leases them to agents.
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/weslo/upool/internal/output"
)

// version is set at build time with -ldflags "-X main.version=<version>".
var version = "dev"

const usage = `usage: upool --version

No commands are implemented yet; docs/design.md lists the planned ones.
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, os.Getenv))
}

// run is upool with its process boundary passed in, so tests call it in-process. It returns the
// exit code.
func run(args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) int {
	if len(args) == 1 && args[0] == "--version" {
		if _, err := fmt.Fprintln(stdout, "upool", version); err != nil {
			return output.ExitFailure
		}
		return output.ExitOK
	}
	_, _ = fmt.Fprint(stderr, usage) // nowhere left to report a failure to write stderr
	return output.ExitFailure
}
