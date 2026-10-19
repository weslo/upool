package ledger

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/weslo/upool/internal/pathvars"
	"github.com/weslo/upool/internal/statefile"
)

var update = flag.Bool("update", false, "rewrite testdata/golden/editors.json")

// golden is the fully populated editors.json, with ${repo} for the repository.
const golden = "../../testdata/golden/editors.json"

// repo is the repository whose slots the tests' Editors have open, and the golden file's ${repo}.
var repo = pathvars.Var{Name: "repo", Path: "/src/down"}

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

// full returns a ledger with every key in docs/contracts.md (e) set: a long-lived Editor upool
// opened, and one running a cold import that it did not.
func full() Ledger {
	return Ledger{
		SchemaVersion: SchemaVersion,
		Editors: []Editor{
			{
				Project:       repo.Path + "/.claude/worktrees/slot-1",
				PID:           51207,
				Mode:          "gui",
				OpenedByUpool: true,
				ChargeGB:      2.5,
			},
			{
				Project:    repo.Path + "/.claude/worktrees/slot-2",
				PID:        51388,
				Mode:       "headless",
				ColdImport: true,
				ChargeGB:   4.5,
			},
		},
	}
}

func load(t *testing.T, home string) Ledger {
	t.Helper()
	l, err := Load(home)
	if err != nil {
		t.Fatalf("Load error = %v, want nil", err)
	}
	return l
}

// save writes l as editors.json through Update.
func save(t *testing.T, home string, l Ledger) {
	t.Helper()
	err := Update(context.Background(), home, func(old *Ledger) error {
		*old = l
		return nil
	})
	if err != nil {
		t.Fatalf("Update error = %v, want nil", err)
	}
}

// unchanged rewrites editors.json through Update without changing anything.
func unchanged(home string) error {
	return Update(context.Background(), home, func(*Ledger) error { return nil })
}

// readFile returns editors.json under home.
func readFile(t *testing.T, home string) string {
	t.Helper()
	data, err := statefile.Read(home, file)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// writeFile replaces editors.json under home with doc.
func writeFile(t *testing.T, home, doc string) {
	t.Helper()
	err := statefile.Update(context.Background(), home, file, func([]byte) ([]byte, error) {
		return []byte(doc), nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// object decodes the JSON object in data into its members.
func object(t *testing.T, data []byte) map[string]json.RawMessage {
	t.Helper()
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		t.Fatal(err)
	}
	return obj
}

func TestStateRoundTripsGolden(t *testing.T) {
	home := tempDir(t)
	save(t, home, full())
	got, err := pathvars.Scrub(readFile(t, home), repo)
	if err != nil {
		t.Fatal(err)
	}
	if *update {
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("editors.json written for full ledger =\n%s\nwant %s:\n%s", got, golden, want)
	}

	doc, err := pathvars.Fill(string(want), repo)
	if err != nil {
		t.Fatal(err)
	}
	home = tempDir(t)
	writeFile(t, home, doc)
	if l := load(t, home); !reflect.DeepEqual(l, full()) {
		t.Errorf("Load of %s =\n%+v\nwant\n%+v", golden, l, full())
	}
}

func TestGoldenHasExactlyTheContractKeys(t *testing.T) {
	data, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	check := func(what string, obj map[string]json.RawMessage, want ...string) {
		t.Helper()
		slices.Sort(want)
		if got := slices.Sorted(maps.Keys(obj)); !slices.Equal(got, want) {
			t.Errorf("%s keys = %q, want %q", what, got, want)
		}
	}
	// The key lists are docs/contracts.md (e).
	doc := object(t, data)
	check("ledger", doc, "schema_version", "editors")
	var editors []json.RawMessage
	if err := json.Unmarshal(doc["editors"], &editors); err != nil {
		t.Fatal(err)
	}
	for _, raw := range editors {
		check("editor", object(t, raw),
			"project", "pid", "mode", "opened_by_upool", "cold_import", "charge_gb")
	}
}

func TestEmptyLedgerIsWrittenWithAnArray(t *testing.T) {
	home := tempDir(t)
	if err := unchanged(home); err != nil {
		t.Fatal(err)
	}
	doc := object(t, []byte(readFile(t, home)))
	if got := string(doc["editors"]); got != "[]" {
		t.Errorf("editors = %s, want []", got)
	}
	if got := string(doc["schema_version"]); got != "1" {
		t.Errorf("schema_version = %s, want 1", got)
	}
}

func TestLoadOfMissingLedgerHasNoEditors(t *testing.T) {
	home := tempDir(t)
	if got := load(t, home); len(got.Editors) != 0 {
		t.Errorf("Load = %+v, want no Editors", got)
	}
	if des, err := os.ReadDir(home); err != nil || len(des) != 0 {
		t.Errorf("home holds %d entries after Load (%v), want none", len(des), err)
	}
}

func TestUpdateWritesEditorsJSON(t *testing.T) {
	home := tempDir(t)
	save(t, home, full())
	data, err := os.ReadFile(filepath.Join(home, ".upool", "editors.json"))
	if err != nil {
		t.Fatalf("reading ~/.upool/editors.json: %v, want the ledger written there", err)
	}
	if got, want := string(data), readFile(t, home); got != want {
		t.Errorf("~/.upool/editors.json = %s, want %s", got, want)
	}
}

func TestUpdateErrorWritesNothing(t *testing.T) {
	home := tempDir(t)
	save(t, home, full())
	before := readFile(t, home)
	refused := errors.New("refused")
	err := Update(context.Background(), home, func(l *Ledger) error {
		l.Editors = nil
		return refused
	})
	if !errors.Is(err, refused) {
		t.Errorf("Update error = %v, want %v", err, refused)
	}
	if after := readFile(t, home); after != before {
		t.Errorf("editors.json after a failed Update =\n%s\nwant it unchanged:\n%s", after, before)
	}
}

func TestUpdateWritesAnOlderDocumentAtTheCurrentVersion(t *testing.T) {
	home := tempDir(t)
	writeFile(t, home, `{"editors":[]}`)
	if err := unchanged(home); err != nil {
		t.Fatal(err)
	}
	if got := load(t, home); got.SchemaVersion != SchemaVersion {
		t.Errorf("after Update, schema_version = %d, want %d", got.SchemaVersion, SchemaVersion)
	}
}

func TestNewerSchemaVersionIsRefused(t *testing.T) {
	home := tempDir(t)
	// A newer upool may add keys too; the version is what the message is about.
	doc := `{"schema_version":2,"editors":[],"added_later":true}`
	writeFile(t, home, doc)
	check := func(op string, err error) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), "schema_version 2") ||
			!strings.Contains(err.Error(), "upgrade upool") || !strings.Contains(err.Error(), shown) {
			t.Errorf("%s error = %v, want %s, schema_version 2 and upgrade upool", op, err, shown)
		}
	}
	_, err := Load(home)
	check("Load", err)
	check("Update", unchanged(home))
	if got := readFile(t, home); got != doc {
		t.Errorf("editors.json after refused Update = %s, want it unchanged", got)
	}
}

func TestUnknownKeyIsAnError(t *testing.T) {
	for name, doc := range map[string]string{
		"top level":    `{"schema_version":1,"editors":[],"extra":1}`,
		"in an editor": `{"schema_version":1,"editors":[{"project":"/src/down","pid":51207,"extra":1}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			home := tempDir(t)
			writeFile(t, home, doc)
			_, err := Load(home)
			if err == nil || !strings.Contains(err.Error(), `unknown field "extra"`) {
				t.Errorf("Load error = %v, want unknown field \"extra\"", err)
			}
			if err := unchanged(home); err == nil {
				t.Error("Update error = nil, want an error")
			}
			if got := readFile(t, home); got != doc {
				t.Errorf("editors.json after refused Update = %s, want it unchanged", got)
			}
		})
	}
}

func TestMalformedDocumentIsAnError(t *testing.T) {
	for name, doc := range map[string]string{
		"not JSON":        `schema_version = 1`,
		"trailing data":   `{"schema_version":1,"editors":[]} {}`,
		"wrong type":      `{"schema_version":1,"editors":{}}`,
		"charge as text":  `{"schema_version":1,"editors":[{"charge_gb":"2.5"}]}`,
		"version as text": `{"schema_version":"1","editors":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			home := tempDir(t)
			writeFile(t, home, doc)
			_, err := Load(home)
			if err == nil || !strings.Contains(err.Error(), shown) {
				t.Errorf("Load error = %v, want one naming %s", err, shown)
			}
			if err := unchanged(home); err == nil {
				t.Error("Update error = nil, want an error")
			}
			if got := readFile(t, home); got != doc {
				t.Errorf("editors.json after refused Update = %s, want it unchanged", got)
			}
		})
	}
}
