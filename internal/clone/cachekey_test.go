package clone

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/weslo/upool/internal/unity/fake"
)

// The tests of the cache key comparison (A20), replaying testdata/captures/unity/cache-key.json
// through the fake unity, and a copy of it with the key changed for the checkout that changes it.

// capturedKey is the key in testdata/captures/unity/cache-key.json.
const capturedKey = "0cce5c29ab901534b6575c7da8c743206ba94cf3ff8898d5f290334719897bf7"

// keyReply is cache-key.json with the exit code cache-key.exit records, or, with key not "", a copy
// of it whose key is key.
func keyReply(t *testing.T, key string) fake.Reply {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "captures", "unity",
		"cache-key.exit"))
	if err != nil {
		t.Fatal(err)
	}
	exit, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	r := fake.Reply{Capture: "cache-key.json", Exit: exit}
	if key != "" {
		r.Replace = []string{capturedKey, key}
	}
	return r
}

// keyEnv is an environment whose PATH has the fake unity u alone.
func keyEnv(u *fake.Unity) []string { return []string{"PATH=" + u.Dir(), "HOME=" + u.Dir()} }

// checkKeyCalls fails the test unless u was called twice with dir positional and no
// --project-path, which `cache key` rejects (testdata/captures/unity/
// cache-key-project-path-invalid.json).
func checkKeyCalls(t *testing.T, u *fake.Unity, dir string) {
	t.Helper()
	calls := u.Calls()
	want := []string{"cache", "key", dir, "--format", "json"}
	if len(calls) != 2 {
		t.Fatalf("unity calls = %+v, want two", calls)
	}
	for _, c := range calls {
		if !slices.Equal(c.Args, want) || slices.Contains(c.Args, "--project-path") {
			t.Errorf("unity %v, want unity %v", c.Args, want)
		}
	}
}

func TestCacheKeyComparesAroundTheCheckout(t *testing.T) {
	dir := t.TempDir()
	for _, tt := range []struct {
		name    string
		after   string // the key after the checkout; "" is the captured one
		changed bool
	}{
		{"equal keys", "", false},
		{"a changed key", strings.Repeat("ab", 32), true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			u := fake.Install(t, keyReply(t, ""), keyReply(t, tt.after))
			ran := false
			changed, note, err := KeyCheck{Env: keyEnv(u)}.Around(t.Context(), dir,
				func() error {
					ran = true
					return nil
				})
			if err != nil || !ran || changed != tt.changed || note != "" {
				t.Errorf("Around = %t, %q, %v, ran %t; want %t, no note, the checkout run",
					changed, note, err, ran, tt.changed)
			}
			checkKeyCalls(t, u, dir)
		})
	}
}

// Without unity on PATH the checkout runs alone, and nothing is compared or said: doctor reports
// a missing Unity CLI.
func TestCacheKeyWithoutUnityComparesNothing(t *testing.T) {
	ran := false
	changed, note, err := KeyCheck{Env: []string{"PATH=" + t.TempDir()}}.Around(t.Context(),
		t.TempDir(), func() error {
			ran = true
			return nil
		})
	if err != nil || !ran || changed || note != "" {
		t.Errorf("Around = %t, %q, %v, ran %t; want no change, no note, the checkout run",
			changed, note, err, ran)
	}
}

// A key that cannot be read leaves the comparison undone and says why; the checkout still runs,
// and its own error is Around's.
func TestCacheKeyThatFailsIsANote(t *testing.T) {
	dir := t.TempDir()
	u := fake.Install(t, fake.Reply{Capture: "cache-key-project-path-invalid.json", Exit: 1})
	ran := false
	changed, note, err := KeyCheck{Env: keyEnv(u)}.Around(t.Context(), dir, func() error {
		ran = true
		return nil
	})
	if err != nil || !ran || changed || !strings.Contains(note, "INVALID_COMMAND_ARGS") ||
		!strings.Contains(note, "reimport_pending is left as it was") {
		t.Errorf("Around = %t, %q, %v, ran %t; want no change and a note naming the error",
			changed, note, err, ran)
	}

	broken := errors.New("checkout failed")
	u = fake.Install(t, keyReply(t, ""))
	if _, _, err := (KeyCheck{Env: keyEnv(u)}).Around(t.Context(), dir,
		func() error { return broken }); !errors.Is(err, broken) {
		t.Errorf("Around with a failing checkout: error %v, want %v", err, broken)
	}
}
