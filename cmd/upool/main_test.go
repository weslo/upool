package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/weslo/upool/internal/output"
)

func runCaptured(args ...string) (code int, stdout, stderr string) {
	var out, errOut bytes.Buffer
	code = run(args, strings.NewReader(""), &out, &errOut, func(string) string { return "" })
	return code, out.String(), errOut.String()
}

func TestVersionPrintsVersionAndSucceeds(t *testing.T) {
	code, stdout, stderr := runCaptured("--version")
	if code != output.ExitOK {
		t.Errorf("exit = %d, want %d", code, output.ExitOK)
	}
	if want := "upool dev\n"; stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
}

func TestAnythingElsePrintsUsageAndFails(t *testing.T) {
	for _, args := range [][]string{nil, {"nope"}, {"take", "a"}, {"--version", "extra"}} {
		code, stdout, stderr := runCaptured(args...)
		if code != output.ExitFailure {
			t.Errorf("%q: exit = %d, want %d", args, code, output.ExitFailure)
		}
		if stdout != "" {
			t.Errorf("%q: stdout = %q, want empty", args, stdout)
		}
		if stderr != usage {
			t.Errorf("%q: stderr = %q, want the usage text", args, stderr)
		}
	}
}
