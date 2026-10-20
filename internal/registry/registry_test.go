package registry

import (
	"context"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/weslo/upool/internal/paths"
	"github.com/weslo/upool/internal/statefile"
)

// tempDir is t.TempDir with its symlinks resolved (on macOS it is under /var, which is
// /private/var).
func tempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// checkout returns a new directory named down, standing in for a repository's main checkout.
func checkout(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(tempDir(t), "down")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// canonical returns p's key, as Add records it.
func canonical(t *testing.T, p string) string {
	t.Helper()
	k, err := paths.Key(p)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func add(t *testing.T, home, path string) Repo {
	t.Helper()
	repo, err := Add(context.Background(), home, path)
	if err != nil {
		t.Fatalf("Add(%q) error = %v, want nil", path, err)
	}
	return repo
}

func lookup(t *testing.T, home, path string) (Repo, bool) {
	t.Helper()
	repo, ok, err := Lookup(home, path)
	if err != nil {
		t.Fatalf("Lookup(%q) error = %v, want nil", path, err)
	}
	return repo, ok
}

// readFile returns registry.json under home.
func readFile(t *testing.T, home string) string {
	t.Helper()
	data, err := statefile.Read(home, file)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// writeFile replaces registry.json under home with doc.
func writeFile(t *testing.T, home, doc string) {
	t.Helper()
	err := statefile.Update(context.Background(), home, file, func([]byte) ([]byte, error) {
		return []byte(doc), nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// keys returns the keys of the JSON object in data, sorted.
func keys(t *testing.T, data []byte) []string {
	t.Helper()
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		t.Fatal(err)
	}
	return slices.Sorted(maps.Keys(obj))
}

func TestKeyIsBasenameAndHashPrefix(t *testing.T) {
	// printf '%s' /src/down | shasum -a 256 prints 7d4c0b4b0f178cf8…
	if got, want := Key("/src/down"), "down-7d4c0b4b"; got != want {
		t.Errorf("Key(/src/down) = %q, want %q", got, want)
	}
	other := Key("/elsewhere/down")
	if !regexp.MustCompile(`^down-[0-9a-f]{8}$`).MatchString(other) {
		t.Errorf("Key(/elsewhere/down) = %q, want down- and 8 hex digits", other)
	}
	if other == Key("/src/down") {
		t.Errorf("Key(/elsewhere/down) = Key(/src/down) = %q, want the paths' hashes to differ", other)
	}
}

func TestAddRecordsTheCanonicalPathAndItsKey(t *testing.T) {
	home, dir := tempDir(t), checkout(t)
	want := Repo{Path: canonical(t, dir), Key: Key(canonical(t, dir))}
	if got := add(t, home, dir); got != want {
		t.Errorf("Add = %+v, want %+v", got, want)
	}
	got, ok := lookup(t, home, dir)
	if !ok || got != want {
		t.Errorf("Lookup = %+v, %v, want %+v, true", got, ok, want)
	}
}

func TestAddWritesExactlyTheContractKeys(t *testing.T) {
	home := tempDir(t)
	add(t, home, checkout(t))
	data := []byte(readFile(t, home))
	if got, want := keys(t, data), []string{"repos", "schema_version"}; !slices.Equal(got, want) {
		t.Errorf("registry.json keys = %q, want %q", got, want)
	}
	var doc struct {
		SchemaVersion json.RawMessage   `json:"schema_version"`
		Repos         []json.RawMessage `json:"repos"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if got := string(doc.SchemaVersion); got != "1" {
		t.Errorf("schema_version = %s, want 1", got)
	}
	if len(doc.Repos) != 1 {
		t.Fatalf("repos has %d entries, want 1", len(doc.Repos))
	}
	if got, want := keys(t, doc.Repos[0]), []string{"key", "path"}; !slices.Equal(got, want) {
		t.Errorf("repos[0] keys = %q, want %q", got, want)
	}
}

func TestAddIsIdempotent(t *testing.T) {
	home, dir := tempDir(t), checkout(t)
	first := add(t, home, dir)
	before := readFile(t, home)
	if second := add(t, home, dir); second != first {
		t.Errorf("second Add = %+v, want %+v", second, first)
	}
	if after := readFile(t, home); after != before {
		t.Errorf("registry.json after second Add =\n%s\nwant it unchanged:\n%s", after, before)
	}
}

func TestAddKeepsEveryRepo(t *testing.T) {
	home := tempDir(t)
	a, b := checkout(t), checkout(t)
	ra, rb := add(t, home, a), add(t, home, b)
	if ra.Key == rb.Key {
		t.Errorf("two checkouts named down share key %q, want different keys", ra.Key)
	}
	for _, c := range []struct {
		path string
		want Repo
	}{{a, ra}, {b, rb}} {
		if got, ok := lookup(t, home, c.path); !ok || got != c.want {
			t.Errorf("Lookup(%q) = %+v, %v, want %+v, true", c.path, got, ok, c.want)
		}
	}
}

func TestAddWritesAnOlderDocumentAtTheCurrentVersion(t *testing.T) {
	home := tempDir(t)
	writeFile(t, home, `{"repos":[]}`)
	add(t, home, checkout(t))
	var doc Registry
	if err := json.Unmarshal([]byte(readFile(t, home)), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.SchemaVersion != SchemaVersion || len(doc.Repos) != 1 {
		t.Errorf("after Add, schema_version = %d with %d repos, want %d with 1",
			doc.SchemaVersion, len(doc.Repos), SchemaVersion)
	}
}

func TestAddRefusesAKeyAnotherPathHas(t *testing.T) {
	home, dir := tempDir(t), checkout(t)
	doc := `{"schema_version":1,"repos":[{"path":"/elsewhere/down","key":"` +
		Key(canonical(t, dir)) + `"}]}`
	writeFile(t, home, doc)
	_, err := Add(context.Background(), home, dir)
	if err == nil || !strings.Contains(err.Error(), "/elsewhere/down") {
		t.Errorf("Add error = %v, want one naming /elsewhere/down", err)
	}
	if got := readFile(t, home); got != doc {
		t.Errorf("registry.json after refused Add = %s, want it unchanged", got)
	}
}

func TestLookupOfMissingRegistryFindsNothing(t *testing.T) {
	home := tempDir(t)
	if got, ok := lookup(t, home, checkout(t)); ok {
		t.Errorf("Lookup = %+v, true, want not found", got)
	}
	if des, err := os.ReadDir(home); err != nil || len(des) != 0 {
		t.Errorf("home holds %d entries after Lookup (%v), want none", len(des), err)
	}
}

func TestListReturnsEveryRepoInTheOrderAdded(t *testing.T) {
	home := tempDir(t)
	got, err := List(home)
	if err != nil || len(got) != 0 {
		t.Errorf("List of a missing registry = %+v, %v, want no repos and nil", got, err)
	}
	if des, err := os.ReadDir(home); err != nil || len(des) != 0 {
		t.Errorf("home holds %d entries after List (%v), want none", len(des), err)
	}
	want := []Repo{add(t, home, checkout(t)), add(t, home, checkout(t))}
	got, err = List(home)
	if err != nil || !slices.Equal(got, want) {
		t.Errorf("List = %+v, %v, want %+v, nil", got, err, want)
	}
}

func TestLookupIsAnExactMatch(t *testing.T) {
	home, dir := tempDir(t), checkout(t)
	add(t, home, dir)
	for _, p := range []string{filepath.Join(dir, "Assets"), filepath.Dir(dir)} {
		if got, ok := lookup(t, home, p); ok {
			t.Errorf("Lookup(%q) = %+v, true, want not found", p, got)
		}
	}
}

func TestLookupMatchesAnotherSpellingOfThePath(t *testing.T) {
	home, dir := tempDir(t), checkout(t)
	want := add(t, home, dir)
	link := filepath.Join(tempDir(t), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{link, dir + "/Assets/..", dir + "/"} {
		if got, ok := lookup(t, home, p); !ok || got != want {
			t.Errorf("Lookup(%q) = %+v, %v, want %+v, true", p, got, ok, want)
		}
	}
}

func TestNewerSchemaVersionIsRefused(t *testing.T) {
	home, dir := tempDir(t), checkout(t)
	// A newer upool may add keys too; the version is what the message is about.
	doc := `{"schema_version":2,"repos":[],"added_later":true}`
	writeFile(t, home, doc)
	check := func(op string, err error) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), "schema_version 2") ||
			!strings.Contains(err.Error(), "upgrade upool") {
			t.Errorf("%s error = %v, want schema_version 2 and upgrade upool", op, err)
		}
	}
	_, _, err := Lookup(home, dir)
	check("Lookup", err)
	_, err = List(home)
	check("List", err)
	_, err = Add(context.Background(), home, dir)
	check("Add", err)
	if got := readFile(t, home); got != doc {
		t.Errorf("registry.json after refused Add = %s, want it unchanged", got)
	}
}

func TestUnknownKeyIsAnError(t *testing.T) {
	for name, doc := range map[string]string{
		"top level": `{"schema_version":1,"repos":[],"extra":1}`,
		"in a repo": `{"schema_version":1,"repos":[{"path":"/src/down","key":"down-7d4c0b4b","extra":1}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			home, dir := tempDir(t), checkout(t)
			writeFile(t, home, doc)
			_, _, err := Lookup(home, dir)
			if err == nil || !strings.Contains(err.Error(), `unknown field "extra"`) {
				t.Errorf("Lookup error = %v, want unknown field \"extra\"", err)
			}
			if _, err := Add(context.Background(), home, dir); err == nil {
				t.Error("Add error = nil, want an error")
			}
			if got := readFile(t, home); got != doc {
				t.Errorf("registry.json after refused Add = %s, want it unchanged", got)
			}
		})
	}
}

func TestMalformedDocumentIsAnError(t *testing.T) {
	for name, doc := range map[string]string{
		"not JSON":      `schema_version = 1`,
		"trailing data": `{"schema_version":1,"repos":[]} {}`,
		"wrong type":    `{"schema_version":1,"repos":{}}`,
		"version text":  `{"schema_version":"1","repos":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			home, dir := tempDir(t), checkout(t)
			writeFile(t, home, doc)
			_, _, err := Lookup(home, dir)
			if err == nil || !strings.Contains(err.Error(), "registry.json") {
				t.Errorf("Lookup error = %v, want one naming registry.json", err)
			}
			if _, err := Add(context.Background(), home, dir); err == nil {
				t.Error("Add error = nil, want an error")
			}
			if got := readFile(t, home); got != doc {
				t.Errorf("registry.json after refused Add = %s, want it unchanged", got)
			}
		})
	}
}

// Containing finds the repository a path is in, the innermost of nested ones, but not from inside
// a repository's .claude/worktrees, which holds slots and agent worktrees (A13).
func TestContainingMatchesAPrefixButNotTheWorktreesDirectory(t *testing.T) {
	home := tempDir(t)
	outer := checkout(t)
	inner := filepath.Join(outer, "packages", "inner")
	slot := filepath.Join(outer, ".claude", "worktrees", "slot-1")
	if err := os.MkdirAll(slot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, repo := range []string{outer, inner} {
		if _, err := Add(context.Background(), home, repo); err != nil {
			t.Fatal(err)
		}
	}
	for _, tt := range []struct {
		path, want string
	}{
		{outer, outer},
		{filepath.Join(outer, "Assets", "Scripts"), outer},
		{filepath.Join(outer, ".claude"), outer},
		{inner, inner},
		{filepath.Join(inner, "src"), inner},
		{filepath.Join(outer, ".claude", "worktrees"), ""},
		{filepath.Join(outer, ".claude", "worktrees", "slot-1"), ""},
		{filepath.Join(outer, ".claude", "worktrees", "slot-1", "Assets"), ""},
		{filepath.Dir(outer), ""},
		{outer + "-sibling", ""},
	} {
		repo, ok, err := Containing(home, tt.path)
		if err != nil {
			t.Fatalf("Containing(%s): %v", tt.path, err)
		}
		switch {
		case tt.want == "" && ok:
			t.Errorf("Containing(%s) = %s, want none", tt.path, repo.Path)
		case tt.want != "" && (!ok || repo.Path != canonical(t, tt.want)):
			t.Errorf("Containing(%s) = %q, %t; want %s", tt.path, repo.Path, ok, tt.want)
		}
	}
	if _, ok, err := Containing(tempDir(t), outer); err != nil || ok {
		t.Errorf("Containing with no registry = %t, %v; want none", ok, err)
	}
}
