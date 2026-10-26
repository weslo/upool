package clone

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/weslo/upool/internal/git"
	"github.com/weslo/upool/internal/ledger"
	"github.com/weslo/upool/internal/pool"
	"github.com/weslo/upool/internal/proc"
	"github.com/weslo/upool/internal/testenv"
)

// The tests of the seed choice (docs/contracts.md (g) step 0; A19), on a testenv repository whose
// slots are real git worktrees with a Library of their own. Liveness is injected: seedProcs knows
// the cloning process and any Editor the test starts.

// seedProcs is a proc.Checker over the processes it lists: PID to start time.
type seedProcs map[int]time.Time

func (f seedProcs) Start(pid int) (time.Time, error) {
	if start, ok := f[pid]; ok {
		return start, nil
	}
	return time.Time{}, proc.ErrNoProcess
}

func (f seedProcs) Alive(pid int, start time.Time) (bool, error) {
	got, ok := f[pid]
	return ok && got.Equal(start), nil
}

// The cloning process, an Editor, and when each started.
const (
	clonerPID = 5100
	editorPID = 5200
)

var procStart = time.Date(2026, 10, 19, 8, 0, 0, 0, time.UTC)

// seedEnv is a testenv repository with a pool of slots, and a Seeder for it.
type seedEnv struct {
	*testenv.Env
	key string
	sd  Seeder
}

// newSeedEnv makes a seedEnv whose pool has a slot per entry of states, slot-1 first, each a
// git worktree detached at base. warm gives each slot's Library the time its assemblies were
// built; a zero time leaves the slot without a Library.
func newSeedEnv(t *testing.T, states []pool.State, warm []time.Time) seedEnv {
	t.Helper()
	e := testenv.New(t)
	procs := seedProcs{clonerPID: procStart}
	const key = "project-00000000"
	var slots []pool.Slot
	for i, state := range states {
		name := "slot-" + string(rune('1'+i))
		path := e.Path(".claude/worktrees/" + name)
		e.Git("worktree", "add", "-q", "--detach", path, testenv.Base)
		if !warm[i].IsZero() {
			warmLibrary(t, path, warm[i])
		}
		s := pool.Slot{Name: name, Path: path, State: state, SeedHead: e.Head}
		if state == pool.StateLeased {
			s.Lease = &pool.Lease{LeaseID: "id-" + name, Confirmed: true, Lane: "script"}
		}
		slots = append(slots, s)
	}
	err := pool.Update(t.Context(), e.Home, key, func(p *pool.Pool) error {
		p.Repo, p.Base, p.Slots = e.Repo, testenv.Base, slots
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return seedEnv{Env: e, key: key, sd: Seeder{Git: git.Client{Runner: git.Exec{}, Env: e.Environ()},
		Proc: procs, OwnerPID: clonerPID, Home: e.Home, Key: key}}
}

// warmLibrary gives the checkout at dir scripts at testenv.ScriptTime and a ScriptAssemblies
// built at built, which is current when built is after testenv.ScriptTime.
func warmLibrary(t *testing.T, dir string, built time.Time) {
	t.Helper()
	err := filepath.WalkDir(filepath.Join(dir, "Assets"), func(path string, d fs.DirEntry,
		err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".cs") {
			return err
		}
		return os.Chtimes(path, testenv.ScriptTime, testenv.ScriptTime)
	})
	if err != nil {
		t.Fatal(err)
	}
	asm := filepath.Join(dir, "Library", "ScriptAssemblies")
	if err := os.MkdirAll(asm, 0o755); err != nil {
		t.Fatal(err)
	}
	dll := filepath.Join(asm, "Assembly-CSharp.dll")
	if err := os.WriteFile(dll, []byte("assembly\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{dll, asm} {
		if err := os.Chtimes(p, built, built); err != nil {
			t.Fatal(err)
		}
	}
}

// slot returns the slot named name in state.json.
func (e seedEnv) slot(t *testing.T, name string) pool.Slot {
	t.Helper()
	p, err := pool.Load(e.Home, e.key)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range p.Slots {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("state.json has no %s", name)
	return pool.Slot{}
}

// Library times: older and newer are both current, newer the fresher.
var (
	older = testenv.LibraryTime
	newer = testenv.LibraryTime.Add(time.Hour)
	none  time.Time
)

// The freshest ready slot comes first, and is held warming by the cloner until Done returns it to
// ready (transitions 23 and 18).
func TestChooseSeedTakesTheFreshestReadySlotAndHoldsItWarming(t *testing.T) {
	e := newSeedEnv(t, []pool.State{pool.StateReady, pool.StateReady, pool.StateLeased},
		[]time.Time{older, newer, newer})
	seed, skipped, err := e.sd.Choose(t.Context())
	if err != nil || seed.Slot != "slot-2" || seed.Path != e.slot(t, "slot-2").Path ||
		len(skipped) != 0 {
		t.Fatalf("Choose = %+v, %q, %v; want slot-2, the fresher, with nothing skipped", seed,
			skipped, err)
	}
	s := e.slot(t, "slot-2")
	if s.State != pool.StateWarming || s.OwnerPID == nil || *s.OwnerPID != clonerPID ||
		s.OwnerPIDStart == nil || !s.OwnerPIDStart.Equal(procStart) {
		t.Errorf("slot-2 while seeding = %s, owner %v; want warming, owned by the cloner",
			s.State, s.OwnerPID)
	}
	if s := e.slot(t, "slot-1"); s.State != pool.StateReady {
		t.Errorf("slot-1 = %s, want it left ready", s.State)
	}
	if err := e.sd.Done(t.Context(), seed); err != nil {
		t.Fatal(err)
	}
	if s := e.slot(t, "slot-2"); s.State != pool.StateReady || s.OwnerPID != nil ||
		s.OwnerPIDStart != nil {
		t.Errorf("slot-2 after the copy = %s, owner %v; want ready with no owner", s.State,
			s.OwnerPID)
	}
}

// Freshness is the Editor's last compile: a slot whose build cache has an assembly newer than the
// other slot's ScriptAssemblies is the fresher, though its own ScriptAssemblies is older (#144).
func TestChooseSeedCountsTheBuildCacheInFreshness(t *testing.T) {
	e := newSeedEnv(t, []pool.State{pool.StateReady, pool.StateReady}, []time.Time{newer, older})
	dag := filepath.Join(e.slot(t, "slot-2").Path, "Library", "Bee", "artifacts", "200b0aEDbg.dag")
	if err := os.MkdirAll(dag, 0o755); err != nil {
		t.Fatal(err)
	}
	dll := filepath.Join(dag, "Assembly-CSharp.dll")
	if err := os.WriteFile(dll, []byte("assembly\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(dll, newer.Add(time.Hour), newer.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	seed, _, err := e.sd.Choose(t.Context())
	if err != nil || seed.Slot != "slot-2" {
		t.Fatalf("Choose = %+v, %v; want slot-2, compiled last", seed, err)
	}
}

// Candidates in A19's order, each skipped for its own reason: a ready slot with a live Editor, a
// stale one, then a needs-reset slot, reset and chosen.
func TestChooseSeedSkipsALiveEditorAndAStaleLibrary(t *testing.T) {
	e := newSeedEnv(t, []pool.State{pool.StateReady, pool.StateReady, pool.StateNeedsReset},
		[]time.Time{newer, none, older})
	e.sd.Proc.(seedProcs)[editorPID] = procStart
	err := ledger.Update(t.Context(), e.Home, func(l *ledger.Ledger) error {
		l.Editors = []ledger.Editor{{Project: e.slot(t, "slot-1").Path, PID: editorPID,
			Mode: "gui", OpenedByUpool: true, ChargeGB: 2.5}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	slot3 := e.slot(t, "slot-3").Path
	e.Git("-C", slot3, "checkout", "-q", "-b", "left-over")
	if err := os.MkdirAll(filepath.Join(slot3, "Empty"), 0o755); err != nil {
		t.Fatal(err)
	}

	seed, skipped, err := e.sd.Choose(t.Context())
	want := []string{"slot-1: an Editor upool opened has it open",
		"slot-2: its Library is stale: it has no Library/ScriptAssemblies"}
	if err != nil || seed.Slot != "slot-3" || strings.Join(skipped, "\n") !=
		strings.Join(want, "\n") {
		t.Fatalf("Choose = %+v, %q, %v; want slot-3, skipping %q", seed, skipped, err, want)
	}
	if got := e.Git("-C", slot3, "rev-parse", "--abbrev-ref", "HEAD"); got != "HEAD" {
		t.Errorf("slot-3 is on %s, want it reset to base, detached", got)
	}
	if s := e.slot(t, "slot-3"); s.State != pool.StateWarming {
		t.Errorf("slot-3 = %s, want warming", s.State)
	}
}

// With no slot qualifying, the main checkout is the seed, held by nothing.
func TestChooseSeedFallsBackToTheMainCheckout(t *testing.T) {
	e := newSeedEnv(t, []pool.State{pool.StateLeased, pool.StateParked}, []time.Time{newer, newer})
	seed, skipped, err := e.sd.Choose(t.Context())
	if err != nil || seed.Path != e.Repo || seed.Slot != "" || len(skipped) != 0 {
		t.Fatalf("Choose = %+v, %q, %v; want the main checkout", seed, skipped, err)
	}
	if err := e.sd.Done(t.Context(), seed); err != nil {
		t.Errorf("Done of the main checkout: %v, want nothing to do", err)
	}
}

// The instance files an Editor that is gone left in the main checkout, upool's after a forced close
// or anyone's after a crash, do not keep it from being the seed (A45; #243).
func TestChooseSeedTakesAMainCheckoutADeadEditorLeft(t *testing.T) {
	e := newSeedEnv(t, []pool.State{pool.StateLeased}, []time.Time{newer})
	e.WriteFile(testenv.EditorInstanceFile, `{"process_id": 9876, "version": "6000.4.11f1"}`+"\n")
	e.WriteFile("Library/ProtocolInstance.json", "[]\n")
	e.WriteFile("Temp/UnityLockfile", "")
	seed, skipped, err := e.sd.Choose(t.Context())
	if err != nil || seed.Path != e.Repo || len(skipped) != 0 {
		t.Errorf("Choose = %+v, %q, %v; want the main checkout", seed, skipped, err)
	}
	if _, err := os.Stat(e.Path(testenv.EditorInstanceFile)); err != nil {
		t.Errorf("%s: %v, want it left as it was: only an Editor upool closed has its files "+
			"removed", testenv.EditorInstanceFile, err)
	}
}

// With the main checkout's Editor open and nothing else qualifying, nothing is chosen, and the
// error says why.
func TestChooseSeedChoosesNothingWhileTheMainCheckoutsEditorIsOpen(t *testing.T) {
	e := newSeedEnv(t, []pool.State{pool.StateLeased}, []time.Time{newer})
	e.WriteFile(testenv.EditorInstanceFile, "{}\n")
	seed, skipped, err := e.sd.Choose(t.Context())
	if !errors.Is(err, ErrNoSeed) || seed != (Seed{}) || len(skipped) != 1 ||
		skipped[0] != "the main checkout: the Unity Editor has it open (Library/EditorInstance.json "+
			"names a running Editor)" {
		t.Errorf("Choose = %+v, %q, %v; want ErrNoSeed, saying the Editor is open", seed,
			skipped, err)
	}
}
