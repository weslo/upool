//go:build !darwin

package main

import (
	"strings"
	"testing"

	"github.com/weslo/upool/internal/output"
)

// Off macOS no file system reports a private size, so status --du fails, saying why, while plain
// status still works.
func TestStatusDuNeedsMacOS(t *testing.T) {
	h := newStatusHome(t)
	r := runStatus(t, h.home, h.down, "--du")
	want := "upool: status --du reads APFS private sizes, which only macOS has: "
	if r.code != output.ExitFailure || !strings.HasPrefix(r.stderr, want) {
		t.Errorf("exit %d, stderr %q; want 1 and %q…", r.code, r.stderr, want)
	}
	checkSucceeded(t, runStatus(t, h.home, h.down))
}
