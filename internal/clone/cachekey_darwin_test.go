package clone

import (
	"strings"
	"testing"

	"github.com/weslo/upool/internal/testenv"
	"github.com/weslo/upool/internal/unity/fake"
)

// The clone recipe compares the slot's cache key across step 7's checkout: equal keys leave
// reimport_pending false, and a changed key sets it and warns (A20).
func TestCacheKeyAcrossTheClonesCheckout(t *testing.T) {
	for _, tt := range []struct {
		name    string
		after   string
		pending bool
	}{
		{"equal keys", "", false},
		{"a changed key", strings.Repeat("cd", 32), true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := testenv.New(t, testenv.SecondCommit())
			excludeWorktrees(e)
			u := fake.Install(t, keyReply(t, ""), keyReply(t, tt.after))
			r := newPool(t, e, CP{})
			r.Keys = KeyCheck{Env: append(e.Environ(), "PATH="+u.Dir())}
			var warnings []string
			r.Warn = func(line string) { warnings = append(warnings, line) }
			slot := mustClone(t, e, r)
			if slot.ReimportPending != tt.pending {
				t.Errorf("reimport_pending = %t, want %t", slot.ReimportPending, tt.pending)
			}
			want := ""
			if tt.pending {
				want = ReimportWarning("slot-1")
			}
			if strings.Join(warnings, "|") != want {
				t.Errorf("warnings = %q, want %q", warnings, want)
			}
			checkKeyCalls(t, u, slot.Path)
		})
	}
}
