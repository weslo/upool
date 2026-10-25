//go:build darwin

package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/weslo/upool/internal/ledger"
	"github.com/weslo/upool/internal/output"
	"github.com/weslo/upool/internal/testenv"
)

// The tests of the mirror of a project's .worktreeinclude into its free slots (A44; #233), on the
// harness pool. They run on macOS only, since the files are copied with cp -cR, macOS's
// clonefile(2) (internal/clone).

// worktreeInclude names a gitignored local notes file and .claude/skills/, as #233's acceptance
// test asks.
const worktreeInclude = "notes.local.md\n.claude/skills/\n"

// includePool makes a harness pool whose base commit ignores notes.local.md, as the main
// checkout's .gitignore does, and tracks a .worktreeinclude holding include.
func includePool(t *testing.T, include string) harnessPool {
	t.Helper()
	e := newInitEnv(t)
	e.WriteFile(".gitignore", readFile(t, e, ".gitignore")+"/notes.local.md\n")
	e.WriteFile(".worktreeinclude", include)
	e.Git("add", ".gitignore", ".worktreeinclude")
	e.Git("commit", "-q", "-m", "Keep local notes")
	e.BaseCommit = e.Git("rev-parse", "HEAD")
	e.Head = e.BaseCommit
	return harnessPoolIn(t, e)
}

// mirrorRun runs `upool args... --json` in p's main checkout, fails the test unless it exits 0,
// and returns its data's mirrored slots.
func mirrorRun(t *testing.T, p harnessPool, args ...string) []mirroredSlot {
	t.Helper()
	r := runIn(t, p.Env, p.Repo, append(args, "--json")...)
	if r.code != output.ExitOK {
		t.Fatalf("%v: exit %d, stdout %q, stderr %q; want 0", args, r.code, r.stdout, r.stderr)
	}
	b, err := json.Marshal(decode(t, r.stdout).Data)
	if err != nil {
		t.Fatal(err)
	}
	var data struct {
		Mirrored []mirroredSlot `json:"mirrored"`
	}
	if err := json.Unmarshal(b, &data); err != nil {
		t.Fatal(err)
	}
	return data.Mirrored
}

// checkSlotFile fails the test unless the slot name has the file rel holding want, or, with want
// empty, has no file rel.
func checkSlotFile(t *testing.T, p harnessPool, name, rel, want string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(p.slot(name), filepath.FromSlash(rel)))
	switch {
	case want == "" && !errors.Is(err, fs.ErrNotExist):
		t.Errorf("%s/%s = %q, %v; want no such file", name, rel, b, err)
	case want != "" && (err != nil || string(b) != want):
		t.Errorf("%s/%s = %q, %v; want %q", name, rel, b, err, want)
	}
}

// checkSlotClean fails the test unless git sees nothing to commit in the slot name.
func checkSlotClean(t *testing.T, p harnessPool, name string) {
	t.Helper()
	if status := p.Git("-C", p.slot(name), "status", "--porcelain"); status != "" {
		t.Errorf("%s: git status = %q, want nothing: the mirror touches only ignored files", name,
			status)
	}
}

// The acceptance test of #233, the mirror: a .worktreeinclude naming a gitignored local notes file
// and .claude/skills/ mirrors the main checkout's copies into each free slot at a top-up, the
// skill link a link still, and at a release; a later top-up, after one is deleted from the main
// checkout, deletes it from the slots. No slot is made dirty.
func TestWorktreeIncludeMirrorsAtTopUpAndRelease(t *testing.T) {
	p := includePool(t, worktreeInclude)
	p.WriteFile("notes.local.md", "notes\n")
	p.WriteFile(".claude/skills/y/SKILL.md", "---\nname: y\n---\n")
	copied := []string{testenv.SkillLink, ".claude/skills/y/SKILL.md", "notes.local.md"}
	want := []mirroredSlot{{Slot: "slot-1", Copied: copied, Deleted: []string{}},
		{Slot: "slot-2", Copied: copied, Deleted: []string{}}}
	if got := mirrorRun(t, p, "refresh"); !reflect.DeepEqual(got, want) {
		t.Errorf("top-up: mirrored = %+v, want %+v", got, want)
	}
	for _, name := range []string{"slot-1", "slot-2"} {
		checkSlotFile(t, p, name, "notes.local.md", "notes\n")
		checkSlotFile(t, p, name, ".claude/skills/y/SKILL.md", "---\nname: y\n---\n")
		link, err := os.Readlink(filepath.Join(p.slot(name), testenv.SkillLink))
		if err != nil || link != testenv.SkillTarget {
			t.Errorf("%s/%s -> %q, %v; want a link to %q", name, testenv.SkillLink, link, err,
				testenv.SkillTarget)
		}
		checkSlotClean(t, p, name)
	}
	if got := mirrorRun(t, p, "refresh"); len(got) != 0 {
		t.Errorf("second top-up: mirrored = %+v, want nothing, since nothing changed", got)
	}

	if err := os.Remove(p.Path(".claude/skills/y/SKILL.md")); err != nil {
		t.Fatal(err)
	}
	want = []mirroredSlot{
		{Slot: "slot-1", Copied: []string{}, Deleted: []string{".claude/skills/y/SKILL.md"}},
		{Slot: "slot-2", Copied: []string{}, Deleted: []string{".claude/skills/y/SKILL.md"}}}
	if got := mirrorRun(t, p, "refresh"); !reflect.DeepEqual(got, want) {
		t.Errorf("top-up after the delete: mirrored = %+v, want %+v", got, want)
	}
	for _, name := range []string{"slot-1", "slot-2"} {
		checkSlotFile(t, p, name, ".claude/skills/y/SKILL.md", "")
	}

	if r := runIn(t, p.Env, p.Repo, "take", "feature-x"); r.code != output.ExitOK {
		t.Fatalf("take: exit %d, stderr %q", r.code, r.stderr)
	}
	p.WriteFile("notes.local.md", "newer notes\n")
	want = []mirroredSlot{
		{Slot: "slot-1", Copied: []string{"notes.local.md"}, Deleted: []string{}},
		{Slot: "slot-2", Copied: []string{"notes.local.md"}, Deleted: []string{}}}
	if got := mirrorRun(t, p, "release", "feature-x"); !reflect.DeepEqual(got, want) {
		t.Errorf("release: mirrored = %+v, want %+v", got, want)
	}
	for _, name := range []string{"slot-1", "slot-2"} {
		checkSlotFile(t, p, name, "notes.local.md", "newer notes\n")
		checkSlotClean(t, p, name)
	}
}

// The acceptance test of #233, a leased slot: a top-up mirrors into the free slot alone, and the
// leased one's copies stay as its session has them.
func TestWorktreeIncludeLeavesALeasedSlot(t *testing.T) {
	p := includePool(t, worktreeInclude)
	if r := runIn(t, p.Env, p.Repo, "take", "feature-x"); r.code != output.ExitOK {
		t.Fatalf("take: exit %d, stderr %q", r.code, r.stderr)
	}
	leased := filepath.Join(p.slot("slot-1"), "notes.local.md")
	if err := os.WriteFile(leased, []byte("the session's notes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p.WriteFile("notes.local.md", "notes\n")
	got := mirrorRun(t, p, "refresh")
	want := []mirroredSlot{{Slot: "slot-2", Copied: []string{testenv.SkillLink,
		"notes.local.md"}, Deleted: []string{}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("mirrored = %+v, want %+v", got, want)
	}
	checkSlotFile(t, p, "slot-1", "notes.local.md", "the session's notes\n")
	checkSlotFile(t, p, "slot-2", "notes.local.md", "notes\n")
}

// The acceptance test of #233, files git sees: a tracked file, one .gitignore names among them, and
// an untracked file git does not ignore, which match the patterns, are never copied, overwritten
// or deleted, in the main checkout or the slot, and the slot stays clean but for its own. A file
// git ignores in only one of the two is never copied either: extra.local.txt, which the main
// checkout's own .gitignore ignores and the slot's does not, and notes.local.md, which the main
// checkout's no longer ignores, so the slot's copy stays.
func TestWorktreeIncludeNeverTouchesTrackedOrUntrackedFiles(t *testing.T) {
	p := includePool(t, worktreeInclude+"*.local.txt\n")
	const forced = ".claude/skills/z/SKILL.md"
	p.WriteFile("tracked.local.txt", "committed\n")
	p.WriteFile(forced, "committed\n")
	p.Git("add", "-f", "tracked.local.txt", forced)
	p.Git("commit", "-q", "-m", "Tracked files the patterns name")
	slot := p.slot("slot-1")
	for _, name := range []string{"slot-1", "slot-2"} {
		p.Git("-C", p.slot(name), "checkout", "-q", "--detach", "main")
	}
	p.WriteFile("tracked.local.txt", "edited in the main checkout\n")
	p.WriteFile(forced, "edited in the main checkout\n")
	p.WriteFile("loose.local.txt", "untracked in the main checkout\n")
	p.WriteFile(".gitignore", readFile(t, p.Env, ".gitignore")+"/extra.local.txt\n!/notes.local.md\n")
	p.WriteFile("extra.local.txt", "ignored in the main checkout alone\n")
	p.WriteFile("notes.local.md", "no longer ignored in the main checkout\n")
	if err := os.WriteFile(filepath.Join(slot, "notes.local.md"), []byte("the slot's notes\n"),
		0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(slot, "own.local.txt"), []byte("the slot's\n"),
		0o644); err != nil {
		t.Fatal(err)
	}
	// The skill link, which git ignores everywhere, is all the mirror copies.
	link := []string{testenv.SkillLink}
	want := []mirroredSlot{{Slot: "slot-1", Copied: link, Deleted: []string{}},
		{Slot: "slot-2", Copied: link, Deleted: []string{}}}
	if got := mirrorRun(t, p, "refresh"); !reflect.DeepEqual(got, want) {
		t.Errorf("mirrored = %+v, want %+v", got, want)
	}
	checkSlotFile(t, p, "slot-1", "tracked.local.txt", "committed\n")
	checkSlotFile(t, p, "slot-1", forced, "committed\n")
	checkSlotFile(t, p, "slot-1", "loose.local.txt", "")
	checkSlotFile(t, p, "slot-1", "own.local.txt", "the slot's\n")
	checkSlotFile(t, p, "slot-1", "extra.local.txt", "")
	checkSlotFile(t, p, "slot-1", "notes.local.md", "the slot's notes\n")
	checkSlotFile(t, p, "slot-2", "notes.local.md", "")
	checkSlotFile(t, p, "slot-2", "loose.local.txt", "")
	for name, want := range map[string]string{"tracked.local.txt": "edited in the main checkout\n",
		forced: "edited in the main checkout\n", "loose.local.txt": "untracked in the main " +
			"checkout\n"} {
		if got := readFile(t, p.Env, name); got != want {
			t.Errorf("the main checkout's %s = %q, want %q", name, got, want)
		}
	}
	if status := p.Git("-C", slot, "status", "--porcelain"); status != "?? own.local.txt" {
		t.Errorf("slot-1: git status = %q, want only its own untracked file", status)
	}
}

// A free slot with an Editor open is not mirrored into, as no slot an Editor has open is moved.
func TestWorktreeIncludeLeavesASlotWithAnEditor(t *testing.T) {
	p := includePool(t, worktreeInclude)
	writeLedger(t, p, ledger.Editor{Project: p.slot("slot-2"), PID: os.Getpid(), Mode: modeGUI,
		ChargeGB: 2.5})
	p.WriteFile("notes.local.md", "notes\n")
	got := mirrorRun(t, p, "refresh")
	if len(got) != 1 || got[0].Slot != "slot-1" {
		t.Errorf("mirrored = %+v, want slot-1 alone", got)
	}
	checkSlotFile(t, p, "slot-2", "notes.local.md", "")
}

// With no .worktreeinclude, nothing is mirrored, and the confirm copies nothing either: a slot's
// stale copy of a gitignored file stays as it is.
func TestWorktreeIncludeAbsentMirrorsNothing(t *testing.T) {
	p := newHarnessPool(t)
	p.WriteFile(".claude/settings.local.json", `{"model": "opus"}`+"\n")
	if got := mirrorRun(t, p, "refresh"); len(got) != 0 {
		t.Errorf("mirrored = %+v, want nothing", got)
	}
	checkSlotFile(t, p, "slot-1", ".claude/settings.local.json", "")
}
