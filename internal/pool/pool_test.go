package pool

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
	"time"

	"github.com/weslo/upool/internal/pathvars"
	"github.com/weslo/upool/internal/statefile"
)

var update = flag.Bool("update", false, "rewrite testdata/golden/state.json")

const (
	// golden is the fully populated state.json, with ${repo} for the repository.
	golden = "../../testdata/golden/state.json"
	// key is the repo key the tests store the pool under, registry.Key("/src/down").
	key = "down-7d4c0b4b"
	// shown is how messages name the pool's state.json.
	shown = "~/.upool/pools/" + key + "/state.json"
)

// repo is the main checkout in the tests' pools, and the golden file's ${repo}.
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

// full returns a pool with every key in docs/contracts.md (e) set, and every timestamp in loc. The
// instants are the same whatever loc is.
func full(loc *time.Location) Pool {
	at := func(hour, minute, second, micro int) time.Time {
		return time.Date(2026, 10, 18, hour, minute, second, micro*1000, time.UTC).In(loc)
	}
	slot := func(n string) string { return repo.Path + "/.claude/worktrees/" + n }
	return Pool{
		SchemaVersion: SchemaVersion,
		Repo:          repo.Path,
		Base:          "main",
		Slots: []Slot{
			{
				Name:            "slot-1",
				Path:            slot("slot-1"),
				State:           StateLeased,
				SeedHead:        "7d4c0b4b0f178cf8e2a1d6b5c3f4e9a8b7c6d5e4",
				SeedDirty:       true,
				ReimportPending: true,
				LastUsed:        at(8, 30, 0, 0),
				Lease: &Lease{
					LeaseID:        "c5e1a9f04b7d2e63",
					Holder:         "0f6b1c2d-3e4f-4a5b-8c6d-7e8f9a0b1c2d",
					WorktreePath:   slot("slot-1"),
					HolderPID:      new(48213),
					HolderPIDStart: new(at(9, 14, 3, 123456)),
					AcquiredAt:     at(9, 14, 5, 0),
					Confirmed:      true,
					Lane:           "scene",
					NameHint:       "fix-lighting",
					SessionID:      new("67f55030-4ba9-4cea-8b2e-7015b8fd7522"),
				},
			},
			{
				Name:          "slot-2",
				Path:          slot("slot-2"),
				State:         StateCreating,
				OwnerPID:      new(48377),
				OwnerPIDStart: new(at(9, 14, 6, 654321)),
				SeedHead:      "e2a1d6b5c3f4e9a8b7c6d5e47d4c0b4b0f178cf8",
				LastUsed:      at(9, 14, 6, 0),
			},
		},
		AgentWorktrees: []AgentWorktree{{
			Path:            slot("agent-0123456789abcdef0"),
			ParentSessionID: "0f6b1c2d-3e4f-4a5b-8c6d-7e8f9a0b1c2d",
			ParentPID:       48102,
			CreatedAt:       at(9, 20, 0, 0),
		}},
		RefreshJob: &RefreshJob{PID: 48390, PIDStart: at(9, 14, 7, 1)},
	}
}

func load(t *testing.T, home string) Pool {
	t.Helper()
	p, err := Load(home, key)
	if err != nil {
		t.Fatalf("Load error = %v, want nil", err)
	}
	return p
}

// save writes p as the pool's state.json through Update.
func save(t *testing.T, home string, p Pool) {
	t.Helper()
	err := Update(context.Background(), home, key, func(old *Pool) error {
		*old = p
		return nil
	})
	if err != nil {
		t.Fatalf("Update error = %v, want nil", err)
	}
}

// unchanged rewrites the pool's state.json through Update without changing anything.
func unchanged(home string) error {
	return Update(context.Background(), home, key, func(*Pool) error { return nil })
}

// readFile returns the pool's state.json under home.
func readFile(t *testing.T, home string) string {
	t.Helper()
	data, err := statefile.Read(home, filepath.Join("pools", key, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// writeFile replaces the pool's state.json under home with doc.
func writeFile(t *testing.T, home, doc string) {
	t.Helper()
	err := statefile.Update(context.Background(), home, filepath.Join("pools", key, "state.json"),
		func([]byte) ([]byte, error) { return []byte(doc), nil })
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

// array decodes the JSON array in data into its elements.
func array(t *testing.T, data []byte) []json.RawMessage {
	t.Helper()
	var arr []json.RawMessage
	if err := json.Unmarshal(data, &arr); err != nil {
		t.Fatal(err)
	}
	return arr
}

func TestStateRoundTripsGolden(t *testing.T) {
	home := tempDir(t)
	save(t, home, full(time.UTC))
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
		t.Errorf("state.json written for full pool =\n%s\nwant %s:\n%s", got, golden, want)
	}

	doc, err := pathvars.Fill(string(want), repo)
	if err != nil {
		t.Fatal(err)
	}
	home = tempDir(t)
	writeFile(t, home, doc)
	if p := load(t, home); !reflect.DeepEqual(p, full(time.UTC)) {
		t.Errorf("Load of %s =\n%+v\nwant\n%+v", golden, p, full(time.UTC))
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
	// The key lists are docs/contracts.md (e), the refresh_job object's from #38.
	pool := object(t, data)
	check("pool", pool,
		"schema_version", "repo", "base", "slots", "agent_worktrees", "refresh_job")
	for i, raw := range array(t, pool["slots"]) {
		slot := object(t, raw)
		check("slot", slot, "name", "path", "state", "owner_pid", "owner_pid_start", "seed_head",
			"seed_dirty", "reimport_pending", "last_used", "lease")
		if i == 0 {
			check("lease", object(t, slot["lease"]), "lease_id", "holder", "worktree_path",
				"holder_pid", "holder_pid_start", "acquired_at", "confirmed", "reset_pending",
				"lane", "name_hint", "session_id")
		}
	}
	for _, raw := range array(t, pool["agent_worktrees"]) {
		check("agent worktree", object(t, raw),
			"path", "parent_session_id", "parent_pid", "created_at")
	}
	check("refresh_job", object(t, pool["refresh_job"]), "pid", "pid_start")
}

func TestTimestampsAreWrittenInUTC(t *testing.T) {
	cest := time.FixedZone("CEST", 2*60*60)
	home := tempDir(t)
	p := full(cest)
	save(t, home, p)
	utcHome := tempDir(t)
	save(t, utcHome, full(time.UTC))
	if got, want := readFile(t, home), readFile(t, utcHome); got != want {
		t.Errorf("state.json for a pool with CEST times =\n%s\nwant the UTC pool's:\n%s", got, want)
	}

	// encode leaves the pool it was given as it was.
	if _, err := encode(p, shown); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p, full(cest)) {
		t.Errorf("pool after encode =\n%+v\nwant it unchanged:\n%+v", p, full(cest))
	}
}

func TestNullFieldsRoundTrip(t *testing.T) {
	home := tempDir(t)
	// A create-hook lease before confirm has no holder PID, and a ready slot no owner or lease.
	p := Pool{
		Repo: repo.Path,
		Base: "main",
		Slots: []Slot{
			{Name: "slot-1", Path: repo.Path + "/.claude/worktrees/slot-1",
				State: StateLeasedUnconfirmed, Lease: &Lease{LeaseID: "c5e1a9f04b7d2e63",
					Holder: "0f6b1c2d", WorktreePath: repo.Path + "/.claude/worktrees/slot-1",
					Lane: "script"}},
			{Name: "slot-2", Path: repo.Path + "/.claude/worktrees/slot-2", State: StateReady},
		},
		AgentWorktrees: []AgentWorktree{},
	}
	save(t, home, p)
	first := readFile(t, home)

	isNull := func(what string, obj map[string]json.RawMessage, k string) {
		t.Helper()
		if got, ok := obj[k]; !ok || string(got) != "null" {
			t.Errorf("%s %s = %s (present %v), want null", what, k, got, ok)
		}
	}
	doc := object(t, []byte(first))
	isNull("pool", doc, "refresh_job")
	slots := array(t, doc["slots"])
	for i, raw := range slots {
		slot := object(t, raw)
		isNull("slot", slot, "owner_pid")
		isNull("slot", slot, "owner_pid_start")
		if i == 0 {
			lease := object(t, slot["lease"])
			isNull("lease", lease, "holder_pid")
			isNull("lease", lease, "holder_pid_start")
		} else {
			isNull("slot", slot, "lease")
		}
	}

	p.SchemaVersion = SchemaVersion
	if got := load(t, home); !reflect.DeepEqual(got, p) {
		t.Errorf("Load =\n%+v\nwant\n%+v", got, p)
	}
	// A rewrite that changes nothing writes the nulls back as they were.
	if err := unchanged(home); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, home); got != first {
		t.Errorf("state.json after an unchanged rewrite =\n%s\nwant\n%s", got, first)
	}
}

func TestEmptyPoolIsWrittenWithArrays(t *testing.T) {
	home := tempDir(t)
	if err := unchanged(home); err != nil {
		t.Fatal(err)
	}
	doc := object(t, []byte(readFile(t, home)))
	for _, k := range []string{"slots", "agent_worktrees"} {
		if got := string(doc[k]); got != "[]" {
			t.Errorf("%s = %s, want []", k, got)
		}
	}
	if got := string(doc["schema_version"]); got != "2" {
		t.Errorf("schema_version = %s, want 2", got)
	}
}

func TestLoadOfMissingStateIsTheZeroPool(t *testing.T) {
	home := tempDir(t)
	if got := load(t, home); !reflect.DeepEqual(got, Pool{}) {
		t.Errorf("Load = %+v, want the zero Pool", got)
	}
	if des, err := os.ReadDir(home); err != nil || len(des) != 0 {
		t.Errorf("home holds %d entries after Load (%v), want none", len(des), err)
	}
}

func TestUpdateWritesUnderThePoolsRepoKey(t *testing.T) {
	home := tempDir(t)
	save(t, home, full(time.UTC))
	data, err := os.ReadFile(filepath.Join(home, ".upool", "pools", key, "state.json"))
	if err != nil {
		t.Fatalf("reading ~/.upool/pools/%s/state.json: %v, want the pool written there", key, err)
	}
	if got, want := string(data), readFile(t, home); got != want {
		t.Errorf("~/.upool/pools/%s/state.json = %s, want %s", key, got, want)
	}
}

func TestUpdateErrorWritesNothing(t *testing.T) {
	home := tempDir(t)
	save(t, home, full(time.UTC))
	before := readFile(t, home)
	refused := errors.New("refused")
	err := Update(context.Background(), home, key, func(p *Pool) error {
		p.Base = "develop"
		return refused
	})
	if !errors.Is(err, refused) {
		t.Errorf("Update error = %v, want %v", err, refused)
	}
	if after := readFile(t, home); after != before {
		t.Errorf("state.json after a failed Update =\n%s\nwant it unchanged:\n%s", after, before)
	}
}

func TestUpdateWritesAnOlderDocumentAtTheCurrentVersion(t *testing.T) {
	home := tempDir(t)
	writeFile(t, home, `{"repo":"/src/down","base":"main","slots":[],"agent_worktrees":[],`+
		`"refresh_job":null}`)
	if err := unchanged(home); err != nil {
		t.Fatal(err)
	}
	if got := load(t, home); got.SchemaVersion != SchemaVersion || got.Repo != repo.Path {
		t.Errorf("after Update, schema_version = %d with repo %q, want %d with %q",
			got.SchemaVersion, got.Repo, SchemaVersion, repo.Path)
	}
}

func TestRepoKeyMustBeOnePathElement(t *testing.T) {
	for _, bad := range []string{"", ".", "..", "down/7d4c0b4b", "../down-7d4c0b4b"} {
		t.Run(bad, func(t *testing.T) {
			home := tempDir(t)
			if _, err := Load(home, bad); err == nil || !strings.Contains(err.Error(), "repo key") {
				t.Errorf("Load(%q) error = %v, want one about the repo key", bad, err)
			}
			err := Update(context.Background(), home, bad, func(*Pool) error { return nil })
			if err == nil || !strings.Contains(err.Error(), "repo key") {
				t.Errorf("Update(%q) error = %v, want one about the repo key", bad, err)
			}
			if des, err := os.ReadDir(home); err != nil || len(des) != 0 {
				t.Errorf("home holds %d entries (%v), want none", len(des), err)
			}
		})
	}
}

// A version-1 document, from before the lease had session_id, reads with the field null and is
// written at version 2 on its next update (A7; A46).
func TestVersionOneIsMigrated(t *testing.T) {
	home := tempDir(t)
	writeFile(t, home, `{"schema_version":1,"repo":"/src/down","base":"main","slots":[{"name":`+
		`"slot-1","path":"/src/down/.claude/worktrees/slot-1","state":"leased","lease":{"lease_id":`+
		`"l1","holder":"h","worktree_path":"/src/down/.claude/worktrees/slot-1","holder_pid":null,`+
		`"holder_pid_start":null,"acquired_at":"2026-10-19T09:14:05Z","confirmed":true,`+
		`"reset_pending":false,"lane":"script","name_hint":""}}],"agent_worktrees":[]}`)
	p, err := Load(home, key)
	if err != nil {
		t.Fatal(err)
	}
	if l := p.Slots[0].Lease; l == nil || l.SessionID != nil {
		t.Fatalf("lease = %+v, want one with no session_id", l)
	}
	if err := unchanged(home); err != nil {
		t.Fatal(err)
	}
	doc := object(t, []byte(readFile(t, home)))
	if got := string(doc["schema_version"]); got != "2" {
		t.Errorf("schema_version after an update = %s, want 2", got)
	}
}

func TestNewerSchemaVersionIsRefused(t *testing.T) {
	home := tempDir(t)
	// A newer upool may add keys too; the version is what the message is about.
	doc := `{"schema_version":3,"repo":"/src/down","slots":[],"added_later":true}`
	writeFile(t, home, doc)
	check := func(op string, err error) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), "schema_version 3") ||
			!strings.Contains(err.Error(), "upgrade upool") || !strings.Contains(err.Error(), shown) {
			t.Errorf("%s error = %v, want %s, schema_version 3 and upgrade upool", op, err, shown)
		}
	}
	_, err := Load(home, key)
	check("Load", err)
	check("Update", unchanged(home))
	if got := readFile(t, home); got != doc {
		t.Errorf("state.json after refused Update = %s, want it unchanged", got)
	}
}

func TestUnknownKeyIsAnError(t *testing.T) {
	const slot = `"name":"slot-1","path":"/src/down/.claude/worktrees/slot-1","state":"leased"`
	const lease = `"lease_id":"c5e1a9f04b7d2e63","holder":"0f6b1c2d","confirmed":true`
	for name, doc := range map[string]string{
		"top level":        `{"schema_version":1,"slots":[],"extra":1}`,
		"in a slot":        `{"schema_version":1,"slots":[{` + slot + `,"extra":1}]}`,
		"in a lease":       `{"schema_version":1,"slots":[{` + slot + `,"lease":{` + lease + `,"extra":1}}]}`,
		"in an agent tree": `{"schema_version":1,"agent_worktrees":[{"path":"/src/down/x","extra":1}]}`,
		"in refresh_job":   `{"schema_version":1,"refresh_job":{"pid":48390,"extra":1}}`,
	} {
		t.Run(name, func(t *testing.T) {
			home := tempDir(t)
			writeFile(t, home, doc)
			_, err := Load(home, key)
			if err == nil || !strings.Contains(err.Error(), `unknown field "extra"`) {
				t.Errorf("Load error = %v, want unknown field \"extra\"", err)
			}
			if err := unchanged(home); err == nil {
				t.Error("Update error = nil, want an error")
			}
			if got := readFile(t, home); got != doc {
				t.Errorf("state.json after refused Update = %s, want it unchanged", got)
			}
		})
	}
}

func TestMalformedDocumentIsAnError(t *testing.T) {
	for name, doc := range map[string]string{
		"not JSON":        `schema_version = 1`,
		"trailing data":   `{"schema_version":1,"slots":[]} {}`,
		"wrong type":      `{"schema_version":1,"slots":{}}`,
		"pid as text":     `{"schema_version":1,"refresh_job":{"pid":"48390"}}`,
		"time not RFC":    `{"schema_version":1,"agent_worktrees":[{"created_at":"18 Oct 2026"}]}`,
		"version as text": `{"schema_version":"1","slots":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			home := tempDir(t)
			writeFile(t, home, doc)
			_, err := Load(home, key)
			if err == nil || !strings.Contains(err.Error(), shown) {
				t.Errorf("Load error = %v, want one naming %s", err, shown)
			}
			if err := unchanged(home); err == nil {
				t.Error("Update error = nil, want an error")
			}
			if got := readFile(t, home); got != doc {
				t.Errorf("state.json after refused Update = %s, want it unchanged", got)
			}
		})
	}
}

// Delete removes the pool's directory, state.json and what is beside it, once fn passes the pool
// it holds; when fn refuses, nothing goes.
func TestDeleteRemovesThePoolsDirectory(t *testing.T) {
	home := tempDir(t)
	save(t, home, Pool{Repo: repo.Path, Base: "main", Slots: []Slot{{Name: "slot-1"}}})
	dir := filepath.Join(home, ".upool", "pools", key)
	if err := os.WriteFile(filepath.Join(dir, "refresh.log"), []byte("log\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	refused := errors.New("refused")
	err := Delete(context.Background(), home, key, func(p Pool) error {
		if len(p.Slots) != 1 || p.Slots[0].Name != "slot-1" {
			t.Errorf("fn got %+v, want the pool with slot-1", p)
		}
		return refused
	})
	if !errors.Is(err, refused) {
		t.Errorf("Delete error = %v, want fn's", err)
	}
	if load(t, home).Repo != repo.Path {
		t.Error("state.json is gone after fn refused")
	}
	if err := Delete(context.Background(), home, key, func(Pool) error { return nil }); err != nil {
		t.Fatalf("Delete error = %v, want nil", err)
	}
	if _, err := os.Lstat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("%s: %v, want it gone", dir, err)
	}
	if _, err := os.Lstat(filepath.Join(home, ".upool", "lock")); err != nil {
		t.Errorf("~/.upool/lock: %v, want it kept", err)
	}
}
