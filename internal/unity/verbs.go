package unity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/weslo/upool/internal/output"
)

// ProjectPathVar is the variable every Unity CLI call carries, set to the slot it targets: the CLI
// honours it for most verbs, and with it set the call never falls back to the cwd, or to the one
// Editor running, and lands in another slot (docs/contracts.md (c)).
const ProjectPathVar = "UNITY_PROJECT_PATH"

// Verb is one Unity CLI verb and how upool targets a slot with it (docs/contracts.md (c)).
type Verb struct {
	// Words are the arguments that name the verb, after `unity`.
	Words []string
	// Positional says the verb takes the project as a positional argument right after Words,
	// because it ignores UNITY_PROJECT_PATH (test, run, build), was never tested with it (close),
	// rejects --project-path (cache key), or was only ever measured with the path given (open,
	// projects verify, vcs doctor).
	Positional bool
}

// The verbs of docs/contracts.md (c), one row each. Every call sets UNITY_PROJECT_PATH; the
// positional ones also pass the slot after the verb, as (c)'s rule says. None is given
// --project-path, which `cache key` rejects (testdata/captures/unity/
// cache-key-project-path-invalid.json).
var (
	VerbCommand   = Verb{Words: []string{"command"}}
	VerbStatus    = Verb{Words: []string{"status"}}
	VerbRecompile = Verb{Words: []string{"recompile"}}
	VerbList      = Verb{Words: []string{"list"}}
	VerbJob       = Verb{Words: []string{"job"}}
	VerbMCP       = Verb{Words: []string{"mcp"}}
	VerbOpen      = Verb{Words: []string{"open"}, Positional: true}
	VerbTest      = Verb{Words: []string{"test"}, Positional: true}
	VerbRun       = Verb{Words: []string{"run"}, Positional: true}
	VerbBuild     = Verb{Words: []string{"build"}, Positional: true}
	VerbClose     = Verb{Words: []string{"close"}, Positional: true}
	VerbCacheKey  = Verb{Words: []string{"cache", "key"}, Positional: true}
	// VerbProjectsVerify checks a project's .meta files, GUIDs, conflict markers and manifest
	// (testdata/captures/unity/projects-verify-pass.json).
	VerbProjectsVerify = Verb{Words: []string{"projects", "verify"}, Positional: true}
	// VerbVCSDoctor checks a repository's VCS set-up for Unity: its ignore rules, LFS and package
	// pinning (testdata/captures/unity/vcs-doctor.json).
	VerbVCSDoctor = Verb{Words: []string{"vcs", "doctor"}, Positional: true}
	// VerbLicenseStatus reports the machine's Unity licence, which belongs to no project
	// (testdata/captures/unity/license-status.json).
	VerbLicenseStatus = Verb{Words: []string{"license", "status"}}
)

// Verbs is the table: every verb upool may run, in docs/contracts.md (c)'s order. A call goes
// through Client.Call with one of them, so no call can miss the targeting rules.
var Verbs = []Verb{VerbCommand, VerbStatus, VerbRecompile, VerbList, VerbJob, VerbMCP, VerbOpen,
	VerbTest, VerbRun, VerbBuild, VerbClose, VerbCacheKey, VerbProjectsVerify, VerbVCSDoctor,
	VerbLicenseStatus}

// Name is the verb as (c) writes it, such as "cache key".
func (v Verb) Name() string { return strings.Join(v.Words, " ") }

// Result is what one Unity CLI command returned.
type Result struct {
	Stdout []byte
	Stderr []byte
	// ExitCode is the CLI's exit status: 0 for success.
	ExitCode int
}

// Runner runs one Unity CLI command, `unity <args…>`, in dir with env as its whole environment
// ("KEY=value" entries). A command that ran and failed is a Result with a non-zero ExitCode and a
// nil error; the error is for a command that could not be run or finish, such as unity missing
// from env's PATH (ErrNoUnity), or ctx ending. Unit tests replace it with recorded output, as
// internal/git's Runner (CONTRIBUTING.md, "Testing").
type Runner interface {
	Run(ctx context.Context, dir string, env []string, args ...string) (Result, error)
}

// ErrNoUnity is Exec's error when env's PATH has no unity: doctor reports the missing CLI, and a
// caller that can carry on without Unity, such as the cache key comparison, does.
var ErrNoUnity = errors.New("unity is not on PATH")

// Exec is the Runner that runs the unity binary found on env's PATH.
type Exec struct{}

var _ Runner = Exec{}

// Run runs `unity args…` in dir, which must be absolute, with env as its whole environment, and
// unity found on env's PATH rather than the process's.
func (Exec) Run(ctx context.Context, dir string, env []string, args ...string) (Result, error) {
	if !filepath.IsAbs(dir) {
		return Result{}, fmt.Errorf("running unity in %q: directory is not absolute", dir)
	}
	path, found := lookPath("unity", env)
	if !found {
		return Result{}, ErrNoUnity
	}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Dir = dir
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	res := Result{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	var exit *exec.ExitError
	if errors.As(err, &exit) && ctx.Err() == nil {
		res.ExitCode = exit.ExitCode()
		return res, nil
	}
	if err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return res, fmt.Errorf("running unity %s: %w", strings.Join(args, " "), err)
	}
	return res, nil
}

// Client runs upool's Unity CLI calls through Runner, each with Env as the CLI's environment and
// UNITY_PROJECT_PATH set to the slot the call targets.
type Client struct {
	Runner Runner
	Env    []string
}

// Reply is one call's result: the --format json reply decoded, with the command's exit code and
// stderr. Err maps a failed reply to upool's error and exit (docs/contracts.md (d)).
type Reply struct {
	Response Response
	ExitCode int
	Stderr   []byte

	// verb and slot are the call's, which Err names.
	verb Verb
	slot string
}

// Call runs verb against the slot at slot (absolute): `unity <verb> [<slot>] <args…> --format
// json`, in the slot, with UNITY_PROJECT_PATH=<slot>, and the slot positional where the verb's
// row in docs/contracts.md (c) says. A decoded failure is a Reply and no error, for the caller to
// read and to map with Reply.Err. A reply whose stdout is not JSON is a failure whatever the exit
// code, carrying the raw stdout and stderr; it exits as a failure (d) does not list (Reply.Err).
func (c Client) Call(ctx context.Context, verb Verb, slot string, args ...string) (Reply, error) {
	argv := slices.Clone(verb.Words)
	if verb.Positional {
		argv = append(argv, slot)
	}
	argv = append(append(argv, args...), "--format", "json")
	res, err := c.Runner.Run(ctx, slot, withProjectPath(c.Env, slot), argv...)
	if err != nil {
		return Reply{}, err
	}
	reply := Reply{ExitCode: res.ExitCode, Stderr: res.Stderr, verb: verb, slot: slot}
	if err := json.Unmarshal(res.Stdout, &reply.Response); err != nil {
		return reply, failure(res.ExitCode == cliExitNoTarget, output.CodeCommandFailed,
			"unity %s %s exited %d with a reply that is not JSON: stdout %q, stderr %q",
			verb.Name(), slot, res.ExitCode, res.Stdout, res.Stderr)
	}
	return reply, nil
}

// withProjectPath is env with UNITY_PROJECT_PATH set to slot, replacing any value env had.
func withProjectPath(env []string, slot string) []string {
	out := slices.DeleteFunc(slices.Clone(env), func(kv string) bool {
		return strings.HasPrefix(kv, ProjectPathVar+"=")
	})
	return append(out, ProjectPathVar+"="+slot)
}
