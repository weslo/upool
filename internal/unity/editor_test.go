package unity

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/weslo/upool/internal/pathvars"
	"github.com/weslo/upool/internal/unity/fake"
)

// Every `unity open` capture holds the CLI's Hub-registry warning, starting with the CLI's own
// "Warning: " (docs/contracts.md (d)). Launch returns it without that prefix, which upool's own
// "upool: warning: " would double (#285), and NotInHubRegistry knows it.
func TestLaunchReturnsTheHubRegistryWarningWithoutTheCLIsPrefix(t *testing.T) {
	captures, err := filepath.Glob(filepath.Join(capturesDir, "open-*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(captures) == 0 {
		t.Fatalf("no open captures found under %s", capturesDir)
	}
	for _, path := range captures {
		name := filepath.Base(path)
		t.Run(name, func(t *testing.T) {
			slot := slotDir(t)
			vars := []pathvars.Var{{Name: "repo", Path: slot}, {Name: "spike", Path: slot}}
			var captured []string
			for _, w := range decodeCapture(t, name).Warnings {
				filled, err := pathvars.Fill(w, vars...)
				if err != nil {
					t.Fatal(err)
				}
				captured = append(captured, filled)
			}
			u := fake.Install(t, fake.Reply{Capture: name, Vars: vars})
			env := []string{"PATH=" + u.Dir(), "HOME=" + u.Dir()}
			_, warnings, err := Client{Runner: Exec{}, Env: env}.Launch(t.Context(), slot, true)
			if err != nil {
				t.Fatal(err)
			}
			if len(captured) != 1 || len(warnings) != 1 {
				t.Fatalf("warnings = %q, captured %q; want the one Hub-registry warning", warnings,
					captured)
			}
			if w := warnings[0]; strings.HasPrefix(w, "Warning") || "Warning: "+w != captured[0] ||
				!NotInHubRegistry(w) {
				t.Errorf("warning = %q, want %q without its \"Warning: \", as NotInHubRegistry "+
					"knows it", w, captured[0])
			}
		})
	}
}

// NotInHubRegistry knows the CLI's Hub-registry warning with its prefix or without, and no other
// warning, even one that names the registry.
func TestNotInHubRegistryKnowsOnlyTheHubRegistryWarning(t *testing.T) {
	for _, tt := range []struct {
		warning string
		want    bool
	}{
		{`Warning: "/p/slot-1" was not found in the Hub project registry, opening as a file path.`,
			true},
		{`"/p/slot-1" was not found in the Hub project registry, opening as a file path.`, true},
		{"did not log the timings: disk full", false},
		{"the Hub project registry could not be read", false},
		{"", false},
	} {
		if got := NotInHubRegistry(tt.warning); got != tt.want {
			t.Errorf("NotInHubRegistry(%q) = %t, want %t", tt.warning, got, tt.want)
		}
	}
}
