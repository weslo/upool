package pool

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// A kept scene list sits in the pool's directory, named after the slot, until it is dropped; a
// slot or repo key that is not one path element is refused (A8).
func TestSceneListIsKeptBesideTheState(t *testing.T) {
	home := tempDir(t)
	ctx := context.Background()
	data := []byte("sceneSetups:\n- path: Assets/Scenes/Main.unity\n")
	if err := SaveSceneList(ctx, home, "repo-key", "slot-1", data); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(home, ".upool", "pools", "repo-key",
		"slot-1.LastSceneManagerSetup.txt"))
	if err != nil || string(raw) != string(data) {
		t.Errorf("kept file = %q, %v; want %q", raw, err, data)
	}
	if got, ok, err := SavedSceneList(home, "repo-key", "slot-1"); err != nil || !ok ||
		string(got) != string(data) {
		t.Errorf("SavedSceneList = %q, %t, %v; want %q", got, ok, err, data)
	}
	if err := DropSceneList(ctx, home, "repo-key", "slot-1"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := SavedSceneList(home, "repo-key", "slot-1"); err != nil || ok {
		t.Errorf("SavedSceneList after the drop = %t, %v; want none", ok, err)
	}
	if err := DropSceneList(ctx, home, "repo-key", "slot-1"); err != nil {
		t.Errorf("dropping a list no longer kept: %v", err)
	}
	for _, bad := range []struct{ key, slot string }{{"repo-key", "../x"}, {"a/b", "slot-1"},
		{"repo-key", ""}} {
		if err := SaveSceneList(ctx, home, bad.key, bad.slot, data); err == nil {
			t.Errorf("SaveSceneList(%q, %q) succeeded, want it refused", bad.key, bad.slot)
		}
	}
}
