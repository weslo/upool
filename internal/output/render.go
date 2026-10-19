package output

import (
	"encoding/json"
	"fmt"
	"io"
)

// Human is data with a human-readable form, which Render prints without --json. For `take` it is
// the path alone (docs/design.md, "One binary, a dozen commands, one JSON file per pool").
type Human interface {
	// Human returns the text to print, with no final newline.
	Human() string
}

// Result is one command's outcome, as Render prints it.
type Result struct {
	// Command is the envelope's command field: the subcommand, such as "which".
	Command string
	// Data is the envelope's data and, without --json, what prints on stdout.
	Data any
	// Warnings are the envelope's warnings and, without --json, print on stderr.
	Warnings []string
	// Err is nil if the command succeeded; otherwise the envelope's one errors[] entry.
	Err error
}

// Render prints r and returns the exit code upool ends with: ExitOK if r.Err is nil, and otherwise
// the exit code of the *Failure in r.Err's chain, or ExitFailure if there is none.
//
// With asJSON, stdout gets the Envelope alone, indented two spaces as the Unity CLI's replies are
// (testdata/captures/unity/), and stderr nothing. Otherwise stdout gets the data's human form,
// and stderr each warning and then the error, one line each after "upool: ". Data that is not
// Human prints as its indented JSON, and nil data prints nothing.
//
// A failure to encode or write the output is reported on stderr and, if r.Err is nil, makes the
// exit code ExitFailure.
func Render(stdout, stderr io.Writer, asJSON bool, r Result) int {
	exit := ExitOK
	var errs []Error
	if r.Err != nil {
		var code Code
		var message string
		exit, code, message = classify(r.Err)
		errs = []Error{{Code: string(code), Message: message}}
	}

	var out string
	if asJSON {
		env := Envelope{
			Success:  r.Err == nil,
			Command:  r.Command,
			Data:     r.Data,
			Errors:   errs,
			Warnings: r.Warnings,
		}
		b, err := json.MarshalIndent(env, "", "  ")
		if err != nil {
			return broken(stderr, exit, fmt.Errorf("encoding the result: %w", err))
		}
		out = string(b) + "\n"
	} else if r.Data != nil {
		text, err := human(r.Data)
		if err != nil {
			return broken(stderr, exit, err)
		}
		if text != "" {
			out = text + "\n"
		}
	}
	if out != "" {
		if _, err := io.WriteString(stdout, out); err != nil {
			exit = broken(stderr, exit, fmt.Errorf("writing the result: %w", err))
		}
	}

	if !asJSON {
		for _, w := range r.Warnings {
			say(stderr, "warning: "+w)
		}
		if r.Err != nil {
			say(stderr, errs[0].Message)
		}
	}
	return exit
}

// human is data's human form: Human() if it has one, and otherwise its indented JSON.
func human(data any) (string, error) {
	if h, ok := data.(Human); ok {
		return h.Human(), nil
	}
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encoding the result: %w", err)
	}
	return string(b), nil
}

// broken reports err, a failure to print the result, on stderr and returns the exit code: the
// command's own if it had already failed, and otherwise ExitFailure.
func broken(stderr io.Writer, exit int, err error) int {
	say(stderr, err.Error())
	if exit == ExitOK {
		return ExitFailure
	}
	return exit
}

// say writes one line to stderr after "upool: ".
func say(stderr io.Writer, line string) {
	_, _ = fmt.Fprintf(stderr, "upool: %s\n", line) // nowhere left to report a failure to write stderr
}
