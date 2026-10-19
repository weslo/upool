//go:build darwin

package paths

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKeyJoinsPrivateTmp(t *testing.T) {
	for _, pair := range [][2]string{
		{"/tmp/x", "/private/tmp/x"},
		{"/var/x", "/private/var/x"},
	} {
		a, err := Key(pair[0])
		if err != nil {
			t.Fatal(err)
		}
		b, err := Key(pair[1])
		if err != nil {
			t.Fatal(err)
		}
		if a != b {
			t.Errorf("Key(%q) = %q, Key(%q) = %q, want one key", pair[0], a, pair[1], b)
		}
		if !strings.HasPrefix(a, "/private/") {
			t.Errorf("Key(%q) = %q, want it under /private", pair[0], a)
		}
	}
}

// TestCaseSensitiveAgreesWithTheVolume checks pathconf's answer against the volume's behaviour: on
// a case-insensitive volume, a file can be opened by its name in another case.
func TestCaseSensitiveAgreesWithTheVolume(t *testing.T) {
	dir := tempDir(t)
	if err := os.WriteFile(filepath.Join(dir, "probe"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	lower, err := os.Stat(filepath.Join(dir, "probe"))
	if err != nil {
		t.Fatal(err)
	}
	upper, err := os.Stat(filepath.Join(dir, "PROBE"))
	ignoresCase := err == nil && os.SameFile(lower, upper)

	got, err := CaseSensitive(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got == ignoresCase {
		t.Errorf("CaseSensitive = %v, want %v (PROBE opens probe: %v)", got, !ignoresCase, ignoresCase)
	}
}
