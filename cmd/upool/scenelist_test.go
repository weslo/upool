package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/weslo/upool/internal/output"
	"github.com/weslo/upool/internal/pool"
	"github.com/weslo/upool/internal/unity/fake"
)

// The tests of the scene list across a headless session (docs/design.md, "Slot lifecycle: seed
// only from a Library current at HEAD, refresh on handout, reset without `-x`"; A8): `upool open
// --headless` keeps slot-1's Library/LastSceneManagerSetup.txt in upool's state directory, and
// `upool close` puts it back once the Editor has exited. The tests write the file's two states: the
// scene list an Editor leaves, and the empty one a headless Editor closed gracefully writes
// (spike §9 G.3).

// The two states of LastSceneManagerSetup.txt.
const (
	sceneList  = "sceneSetups:\n- path: Assets/Down/States/Startup/Startup.unity\n  isLoaded: 1\n"
	emptyScene = "sceneSetups: []\n"
)

// sceneListPath is slot-1's LastSceneManagerSetup.txt, and keptPath the copy of it upool keeps.
func sceneListPath(p harnessPool) string {
	return filepath.Join(p.slot("slot-1"), "Library", pool.SceneListFile)
}

func keptPath(p harnessPool) string {
	return filepath.Join(p.Home, ".upool", "pools", p.key, "slot-1."+pool.SceneListFile)
}

// writeSceneList writes slot-1's LastSceneManagerSetup.txt.
func writeSceneList(t *testing.T, p harnessPool, content string) {
	t.Helper()
	if err := os.WriteFile(sceneListPath(p), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// readOrNone returns the file at path, or "<none>" when it does not exist.
func readOrNone(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "<none>"
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// openAndClose runs `upool open args...` in slot-1, then calls between, then `upool close`, each
// through the fake unity, failing the test unless both succeed.
func openAndClose(t *testing.T, p harnessPool, openPID int, between func(), args ...string) {
	t.Helper()
	now := func() time.Time { return clock }
	if r := runOpen(t, p, procsAlive(os.Getpid(), openPID), now, args...); r.code != output.ExitOK {
		t.Fatalf("open %v: exit %d, stderr %q", args, r.code, r.stderr)
	}
	between()
	// The Editor's processes are gone by the time `unity close` returns.
	if r := runInSlot1(t, p, procsAlive(), now, "close"); r.code != output.ExitOK {
		t.Fatalf("close: exit %d, stderr %q", r.code, r.stderr)
	}
}

// The acceptance test of #91: after `open --headless` and `close` through the fake unity, with
// the test emptying the file in between as the headless Editor does, the original scene list is
// back, and the copy upool kept is gone.
func TestHeadlessKeepsSceneList(t *testing.T) {
	p := warmPool(t)
	writeSceneList(t, p, sceneList)
	p.installUnity(t, openReply(t, p.Env, "open-headless.json"), statusReady, saveAllReply,
		fake.Reply{Capture: "close-headless.json", Vars: spikeVars})
	openAndClose(t, p, capturedPID, func() {
		if got := readOrNone(t, keptPath(p)); got != sceneList {
			t.Errorf("kept during the session: %q, want the scene list", got)
		}
		writeSceneList(t, p, emptyScene)
	}, "--headless")
	if got := readOrNone(t, sceneListPath(p)); got != sceneList {
		t.Errorf("after close, %s = %q, want the scene list back", pool.SceneListFile, got)
	}
	if got := readOrNone(t, keptPath(p)); got != "<none>" {
		t.Errorf("after close, the copy is still kept: %q", got)
	}
}

// A GUI Editor keeps its scene list itself, so a GUI open keeps no copy and its close restores
// nothing, not even a copy an earlier headless session left: what the Editor wrote stays.
func TestGUILeavesSceneListAlone(t *testing.T) {
	p := warmPool(t)
	writeSceneList(t, p, sceneList)
	if err := pool.SaveSceneList(t.Context(), p.Home, p.key, "slot-1",
		[]byte("stale\n")); err != nil {
		t.Fatal(err)
	}
	p.installUnity(t, openReply(t, p.Env, "open-gui-streams.json"), statusReady, saveAllReply,
		closeReply)
	const changed = "sceneSetups:\n- path: Assets/Other.unity\n  isLoaded: 1\n"
	openAndClose(t, p, capturedGUIPID, func() {
		if got := readOrNone(t, keptPath(p)); got != "stale\n" {
			t.Errorf("a GUI open changed the kept copy to %q", got)
		}
		writeSceneList(t, p, changed)
	}, "--gui")
	if got := readOrNone(t, sceneListPath(p)); got != changed {
		t.Errorf("after close, %s = %q, want what the Editor wrote", pool.SceneListFile, got)
	}
}

// A headless open of a slot with no scene list keeps nothing, and drops a copy an earlier session
// left, so that its close cannot restore a stale list; a headless open that fails drops the copy
// it made.
func TestHeadlessSceneListIsNeverStale(t *testing.T) {
	t.Run("no scene list", func(t *testing.T) {
		p := warmPool(t)
		if err := pool.SaveSceneList(t.Context(), p.Home, p.key, "slot-1",
			[]byte("stale\n")); err != nil {
			t.Fatal(err)
		}
		p.installUnity(t, openReply(t, p.Env, "open-headless.json"), statusReady)
		r := runOpen(t, p, procsAlive(os.Getpid(), capturedPID),
			func() time.Time { return clock })
		if r.code != output.ExitOK || readOrNone(t, keptPath(p)) != "<none>" {
			t.Errorf("exit %d, kept %q; want 0 and no copy", r.code, readOrNone(t, keptPath(p)))
		}
	})
	t.Run("a failed open", func(t *testing.T) {
		p := warmPool(t)
		writeSceneList(t, p, sceneList)
		p.installUnity(t, fake.Reply{Capture: "version.raw.txt", Exit: 1})
		r := runOpen(t, p, procsAlive(os.Getpid()), func() time.Time { return clock })
		if r.code != output.ExitFailure || readOrNone(t, keptPath(p)) != "<none>" {
			t.Errorf("exit %d, kept %q; want 1 and no copy", r.code, readOrNone(t, keptPath(p)))
		}
	})
}
