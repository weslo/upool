package output

import (
	"errors"
	"fmt"
)

// Code is an upool error code, an Envelope's errors[].code. Codes are UPPER_SNAKE, as the Unity
// CLI's are (docs/contracts.md (d)). Every code upool reports of its own is a constant here; a
// Unity CLI failure keeps the CLI's code, which upool passes through (A1).
type Code string

const (
	// CodeCommandFailed is the code of a failure with no more specific one, and the code of any
	// error that is not a *Failure. It is the Unity CLI's own catch-all (docs/contracts.md (d),
	// "Codes upool must handle"), so it means the same in either tool's envelope.
	CodeCommandFailed Code = "COMMAND_FAILED"
	// CodeNotInSlot means the working directory is not inside a slot, which `upool which` reports
	// with ExitFailure (docs/contracts.md (d), "upool's own exit codes").
	CodeNotInSlot Code = "NOT_IN_SLOT"
	// CodeNoSuchSlot means no slot has the branch `upool path` names checked out (A18), which it
	// reports with ExitFailure.
	CodeNoSuchSlot Code = "NO_SUCH_SLOT"
)

// Failure is an error a command returns: the exit code upool ends with, the code its envelope
// reports, and a message. Only Fail, NoTarget and Exited make one, so its exit code is
// ExitFailure, ExitNoTarget, or the status of a command upool ran and passes on: a failure never
// exits ExitOK, and upool's own failures exit with no codes but ExitFailure and ExitNoTarget
// (docs/contracts.md (d), "upool's own exit codes").
type Failure struct {
	exit int
	code Code
	err  error
}

// Fail returns a failure that exits ExitFailure. format and args are as for fmt.Errorf, so a cause
// wrapped with %w stays visible to errors.Is and errors.As.
func Fail(code Code, format string, args ...any) error {
	return &Failure{exit: ExitFailure, code: code, err: fmt.Errorf(format, args...)}
}

// NoTarget returns a failure that exits ExitNoTarget, upool's mirror of the Unity CLI's exit 6: no
// Editor instance, or no Unity project, at the resolved target (docs/contracts.md (d)). format and
// args are as for Fail.
func NoTarget(code Code, format string, args ...any) error {
	return &Failure{exit: ExitNoTarget, code: code, err: fmt.Errorf(format, args...)}
}

// maxStatus is the highest exit status a process can end with.
const maxStatus = 255

// Exited returns a failure that exits with status, the exit status of a command upool ran for its
// caller and passes on: `upool run` ends with its command's (#70). A status of 0, or one no
// process can end with, becomes ExitFailure, so that a failure still never exits ExitOK. Its code
// is CodeCommandFailed; format and args are as for Fail.
func Exited(status int, format string, args ...any) error {
	if status <= ExitOK || status > maxStatus {
		status = ExitFailure
	}
	return &Failure{exit: status, code: CodeCommandFailed, err: fmt.Errorf(format, args...)}
}

// Error returns the message.
func (e *Failure) Error() string { return e.err.Error() }

// Unwrap returns the message as an error, through which a cause wrapped with %w is found.
func (e *Failure) Unwrap() error { return e.err }

// Exit returns the exit code: ExitFailure, ExitNoTarget, or the status Exited passes on.
func (e *Failure) Exit() int { return e.exit }

// Code returns the envelope's errors[].code.
func (e *Failure) Code() Code { return e.code }

// classify returns the exit code, code and message Render reports for err. The exit code and code
// come from the first *Failure in err's chain, so a caller can add context with
// fmt.Errorf("taking %s: %w", name, err) and keep both; any other error is ExitFailure and
// CodeCommandFailed. The message is err's whole text, context included.
func classify(err error) (exit int, code Code, message string) {
	var e *Failure
	if errors.As(err, &e) {
		return e.exit, e.code, err.Error()
	}
	return ExitFailure, CodeCommandFailed, err.Error()
}
