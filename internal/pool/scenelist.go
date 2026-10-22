package pool

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/weslo/upool/internal/statefile"
)

// SceneListFile is the file in a slot's Library/ where Unity keeps the scenes its Editor last had
// open, which a headless Editor closed gracefully empties to `sceneSetups: []` (spike §9 G.3).
const SceneListFile = "LastSceneManagerSetup.txt"

// SaveSceneList keeps data, a slot's Library/LastSceneManagerSetup.txt, across a headless session
// of the slot named slot: in ~/.upool/pools/<repoKey>/<slot>.LastSceneManagerSetup.txt under home,
// outside the slot, so neither a reset nor `git clean` touches it, written under ~/.upool/lock
// (docs/contracts.md A8).
func SaveSceneList(ctx context.Context, home, repoKey, slot string, data []byte) error {
	file, err := sceneListFile(repoKey, slot)
	if err != nil {
		return err
	}
	return statefile.Update(ctx, home, file, func([]byte) ([]byte, error) { return data, nil })
}

// SavedSceneList returns the scene list SaveSceneList kept for the slot named slot, and false when
// none is kept.
func SavedSceneList(home, repoKey, slot string) ([]byte, bool, error) {
	file, err := sceneListFile(repoKey, slot)
	if err != nil {
		return nil, false, err
	}
	if _, err := os.Stat(filepath.Join(home, ".upool", file)); errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	data, err := statefile.Read(home, file)
	return data, err == nil, err
}

// DropSceneList deletes the scene list kept for the slot named slot, if there is one, under
// ~/.upool/lock.
func DropSceneList(ctx context.Context, home, repoKey, slot string) error {
	file, err := sceneListFile(repoKey, slot)
	if err != nil {
		return err
	}
	l, err := statefile.Lock(ctx, home)
	if err != nil {
		return err
	}
	err = os.Remove(filepath.Join(home, ".upool", file))
	if errors.Is(err, fs.ErrNotExist) {
		err = nil
	}
	return errors.Join(err, l.Unlock())
}

// sceneListFile is the kept scene list's path relative to ~/.upool, as internal/statefile takes
// it. repoKey and slot must each be one path element, so that it never lands outside the pool's
// directory.
func sceneListFile(repoKey, slot string) (string, error) {
	file, _, err := stateFile(repoKey)
	if err != nil {
		return "", err
	}
	if slot == "" || slot == "." || slot == ".." || filepath.Base(slot) != slot {
		return "", fmt.Errorf("slot name %q is not a single path element", slot)
	}
	return filepath.Join(filepath.Dir(file), slot+"."+SceneListFile), nil
}
