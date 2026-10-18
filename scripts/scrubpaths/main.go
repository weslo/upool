// Command scrubpaths replaces machine paths in a new capture with the variables in
// testdata/README.md. It reads stdin and writes stdout:
//
//	go run ./scripts/scrubpaths --var repo=/path/to/repo [--var name=path]... < raw > capture
//
// After the --var flags it adds home (from $HOME) and claude_tmp (/private/tmp/claude-<uid>), so a
// --var naming the same path wins, and for every path it also scrubs the symlink-resolved form
// (/tmp is /private/tmp on macOS). It writes nothing and exits 1 if a path that `make check-paths`
// rejects would remain.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/weslo/upool/internal/pathvars"
)

type varFlags []pathvars.Var

func (v *varFlags) String() string { return fmt.Sprint(*v) }

func (v *varFlags) Set(s string) error {
	name, path, ok := strings.Cut(s, "=")
	if !ok || name == "" || path == "" {
		return fmt.Errorf("want name=path, got %q", s)
	}
	*v = append(*v, pathvars.Var{Name: name, Path: path})
	return nil
}

func main() {
	var vars varFlags
	flag.Var(&vars, "var", "a variable as name=path; repeat for each")
	flag.Parse()

	if home, err := os.UserHomeDir(); err == nil {
		vars = append(vars, pathvars.Var{Name: "home", Path: home})
	}
	vars = append(vars, pathvars.Var{Name: "claude_tmp", Path: "/private/tmp/claude-" + strconv.Itoa(os.Getuid())})

	all := make([]pathvars.Var, 0, 2*len(vars))
	for _, v := range vars {
		all = append(all, v)
		if resolved, err := filepath.EvalSymlinks(v.Path); err == nil && resolved != v.Path {
			all = append(all, pathvars.Var{Name: v.Name, Path: resolved})
		}
	}

	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, "scrubpaths:", err)
		os.Exit(1)
	}
	out, err := pathvars.Scrub(string(raw), all...)
	if err != nil {
		fmt.Fprintln(os.Stderr, "scrubpaths:", err)
		os.Exit(1)
	}
	fmt.Print(out)
}
