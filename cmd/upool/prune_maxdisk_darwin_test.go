package main

import (
	"testing"

	"github.com/weslo/upool/internal/config"
)

// prune measures the pool as status --du does when no test injects a measure: with max_disk 0,
// the real private sizes of the three slots' checkouts are over it, so slot-3 and then slot-2 are
// listed, least recently used first, and slot-1 stays by ready.
func TestPruneMaxDiskMeasuresThePool(t *testing.T) {
	p := maxDiskPool(t)
	p.Setenv(config.EnvName("max_disk"), "0")
	r := runPrune(t, p, "--json")
	var data pruneReport
	decodeData(t, r.stdout, &data)
	if len(data.Pools) != 1 {
		t.Fatalf("pools = %+v, want one", data.Pools)
	}
	if got := slotsOf(data.Pools[0].Candidates); len(got) != 2 || got[0] != "slot-3" ||
		got[1] != "slot-2" {
		t.Errorf("candidates = %q, want slot-3 then slot-2", got)
	}
}
