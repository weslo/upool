package unity

import (
	"errors"
	"strings"

	"github.com/weslo/upool/internal/output"
)

// The Unity CLI's errors[].code values that docs/contracts.md (d), "Codes upool must handle",
// lists. COMMAND_FAILED is the CLI's catch-all, and upool's own (output.CodeCommandFailed).
const (
	codeStatusNoInstances  = "STATUS_NO_INSTANCES"
	codeCommandFailed      = string(output.CodeCommandFailed)
	codeAmbiguousEditor    = "AMBIGUOUS_EDITOR"
	codeInvalidCommandArgs = "INVALID_COMMAND_ARGS"
)

// cliExitNoTarget is the Unity CLI's exit when no Editor, or no Unity project, is at the target,
// which upool mirrors with output.ExitNoTarget (docs/contracts.md (d); A3).
const cliExitNoTarget = 6

// ErrMayHaveRun marks the failure of a call that may have done its work anyway: the main-thread
// timeout, which the first eval on a freshly opened GUI Editor met while its code ran (spike §8
// C.3). A caller finds it with errors.Is and re-queries the call's side effects instead of retrying
// it (docs/design.md, "Editors: budget by RAM, serialize cold imports as a courtesy, target by
// variable and by path").
var ErrMayHaveRun = errors.New("it may have run anyway, so check its side effects before " +
	"retrying it")

// knownFailure is one Unity CLI failure of docs/contracts.md (d), "Codes upool must handle", found
// by its errors[].code and, where one code covers several failures, by text in its message.
type knownFailure struct {
	code   string
	marker string
	// noTarget is whether (d) records the failure with exit 6, which upool mirrors with
	// output.ExitNoTarget (A3).
	noTarget bool
	// mayHaveRun marks the main-thread timeout (ErrMayHaveRun).
	mayHaveRun bool
}

// knownFailures are (d)'s failures, in its order. Each is read by its code and message alone,
// whatever the CLI exited with (A2).
var knownFailures = []knownFailure{
	{code: codeStatusNoInstances, noTarget: true},
	{code: codeCommandFailed, marker: "No Pipeline instance found for project", noTarget: true},
	{code: codeCommandFailed, marker: "Not a Unity project", noTarget: true},
	// Its exit was never recorded; it is not a missing target, so it exits ExitFailure.
	{code: codeCommandFailed, marker: "Main thread operation timed out", mayHaveRun: true},
	{code: codeAmbiguousEditor, noTarget: true},
	{code: codeInvalidCommandArgs},
}

// Err returns nil for a reply that succeeded, and otherwise the error upool fails with for it
// (docs/contracts.md (d); A1; A2; A3). A reply failed when success is false or errors[] has an
// entry, whatever the exit code (A2).
//
// The error is an output failure with the CLI's first errors[].code as its code and every entry in
// its message: the CLI's errors pass through (A1). It exits ExitNoTarget for a failure (d) records
// with exit 6, and ExitFailure for the others; a failure (d) does not list exits ExitNoTarget when
// the CLI exited 6, and ExitFailure for any other exit, 7 included (A1; A3). The main-thread
// timeout's error also wraps ErrMayHaveRun.
func (r Reply) Err() error {
	res := r.Response
	if res.Success && len(res.Errors) == 0 {
		return nil
	}
	call := "unity " + r.verb.Name() + " " + r.slot
	if len(res.Errors) == 0 {
		return failure(r.ExitCode == cliExitNoTarget, output.CodeCommandFailed,
			"%s failed with no errors, exiting %d", call, r.ExitCode)
	}
	entries := make([]string, len(res.Errors))
	for i, e := range res.Errors {
		entries[i] = e.Code + ": " + e.Message
	}
	message := call + ": " + strings.Join(entries, "; ")
	first := res.Errors[0]
	code := output.Code(first.Code)
	known, ok := lookUp(first)
	switch {
	case !ok:
		return failure(r.ExitCode == cliExitNoTarget, code, "%s", message)
	case known.mayHaveRun:
		return output.Fail(code, "%s; %w", message, ErrMayHaveRun)
	}
	return failure(known.noTarget, code, "%s", message)
}

// lookUp returns the known failure e is, if (d) lists it.
func lookUp(e Error) (knownFailure, bool) {
	for _, k := range knownFailures {
		if e.Code == k.code && strings.Contains(e.Message, k.marker) {
			return k, true
		}
	}
	return knownFailure{}, false
}

// failure is an output failure that exits ExitNoTarget when noTarget, and ExitFailure otherwise.
func failure(noTarget bool, code output.Code, format string, args ...any) error {
	if noTarget {
		return output.NoTarget(code, format, args...)
	}
	return output.Fail(code, format, args...)
}
