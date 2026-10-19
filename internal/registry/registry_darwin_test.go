//go:build darwin

package registry

import (
	"os"
	"strings"
	"testing"
)

// TestLookupMatchesPrivateTmpSpelling registers a checkout under one spelling of /tmp and looks it
// up under the other. macOS spells the directory both ways, and the Unity CLI reports the /private
// one (testdata/captures/unity/test-not-a-unity-project.json).
func TestLookupMatchesPrivateTmpSpelling(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "upool-registry-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Error(err)
		}
	})
	if !strings.HasPrefix(dir, "/tmp/") {
		t.Fatalf("MkdirTemp(/tmp) = %q, want it under /tmp", dir)
	}
	private := "/private" + dir
	for _, c := range [][2]string{{dir, private}, {private, dir}} {
		home := tempDir(t)
		want := add(t, home, c[0])
		if got, ok := lookup(t, home, c[1]); !ok || got != want {
			t.Errorf("after Add(%q), Lookup(%q) = %+v, %v, want %+v, true", c[0], c[1], got, ok, want)
		}
	}
}
