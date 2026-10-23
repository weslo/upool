//go:build !darwin

package main

import (
	"strings"
	"testing"

	"github.com/weslo/upool/internal/config"
	"github.com/weslo/upool/internal/output"
)

// Off macOS no private size can be read, so prune refuses while max_disk is set, even on a pool
// with nothing to measure, and works without it.
func TestPruneMaxDiskNeedsMacOS(t *testing.T) {
	p := prunePool(t)
	p.Setenv(config.EnvName("ready"), "2")
	p.Setenv(config.EnvName("max_disk"), "10")
	r := runIn(t, p.Env, p.Repo, "prune")
	want := "upool: max_disk is set, from env, but a pool's disk use is measured in APFS private " +
		"sizes"
	if r.code != output.ExitFailure || !strings.HasPrefix(r.stderr, want) {
		t.Errorf("exit %d, stderr %q; want 1 and %q…", r.code, r.stderr, want)
	}
	p.Setenv(config.EnvName("max_disk"), "")
	runPrune(t, p)
}
