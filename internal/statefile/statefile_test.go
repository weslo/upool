package statefile

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
)

// cancelled returns a context that has already ended. Lock with it succeeds only when the lock is
// free, so it checks that nothing holds the lock without waiting for anything.
func cancelled() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

// entries returns the names in dir, sorted.
func entries(t *testing.T, dir string) []string {
	t.Helper()
	des, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, de := range des {
		names = append(names, de.Name())
	}
	return names
}

// mode returns the permission bits of path.
func mode(t *testing.T, path string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

// set returns an Update function that ignores what is there and writes data.
func set(data string) func([]byte) ([]byte, error) {
	return func([]byte) ([]byte, error) { return []byte(data), nil }
}

func TestLockCreatesUpoolDirectoryWithMode0700(t *testing.T) {
	home := t.TempDir()
	l, err := Lock(context.Background(), home)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := l.Unlock(); err != nil {
			t.Error(err)
		}
	}()
	dir := filepath.Join(home, ".upool")
	if got := mode(t, dir); got != 0o700 {
		t.Errorf("~/.upool mode = %v, want %v", got, os.FileMode(0o700))
	}
	if got, want := entries(t, dir), []string{"lock"}; !slices.Equal(got, want) {
		t.Errorf("~/.upool holds %q, want %q", got, want)
	}
}

func TestLockSucceedsAgainAfterUnlock(t *testing.T) {
	home := t.TempDir()
	for i := range 2 {
		l, err := Lock(cancelled(), home)
		if err != nil {
			t.Fatalf("Lock %d error = %v, want nil", i+1, err)
		}
		if err := l.Unlock(); err != nil {
			t.Fatalf("Unlock %d error = %v, want nil", i+1, err)
		}
	}
}

func TestReadOfMissingFileIsEmptyAndCreatesNothing(t *testing.T) {
	home := t.TempDir()
	got, err := Read(home, "registry.json")
	if err != nil || len(got) != 0 {
		t.Errorf("Read = %q, %v, want empty, nil", got, err)
	}
	if names := entries(t, home); len(names) != 0 {
		t.Errorf("home holds %q after Read, want nothing", names)
	}
}

func TestUpdateOfMissingFileStartsEmpty(t *testing.T) {
	home := t.TempDir()
	var old []byte
	err := Update(context.Background(), home, "registry.json", func(b []byte) ([]byte, error) {
		old = b
		return []byte(`{"schema_version":1}`), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(old) != 0 {
		t.Errorf("fn got %q, want empty", old)
	}
	got, err := Read(home, "registry.json")
	if err != nil || string(got) != `{"schema_version":1}` {
		t.Errorf("Read = %q, %v, want %q, nil", got, err, `{"schema_version":1}`)
	}
}

func TestUpdatePassesTheCurrentContents(t *testing.T) {
	home := t.TempDir()
	if err := Update(context.Background(), home, "editors.json", set("first")); err != nil {
		t.Fatal(err)
	}
	var old []byte
	err := Update(context.Background(), home, "editors.json", func(b []byte) ([]byte, error) {
		old = b
		return []byte("second"), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(old) != "first" {
		t.Errorf("fn got %q, want %q", old, "first")
	}
	got, err := Read(home, "editors.json")
	if err != nil || string(got) != "second" {
		t.Errorf("Read = %q, %v, want %q, nil", got, err, "second")
	}
}

// TestUpdateCreatesPoolDirectoriesPrivately checks the per-pool layout, ~/.upool/pools/<repo-key>/
// state.json (docs/contracts.md (e) State schemas): directories 0700, the file 0600, and no
// temporary file left beside it.
func TestUpdateCreatesPoolDirectoriesPrivately(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join("pools", "game-0123abcd", "state.json")
	if err := Update(context.Background(), home, path, set("{}")); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"pools", filepath.Join("pools", "game-0123abcd")} {
		if got := mode(t, filepath.Join(home, ".upool", dir)); got != 0o700 {
			t.Errorf("%s mode = %v, want %v", dir, got, os.FileMode(0o700))
		}
	}
	if got := mode(t, filepath.Join(home, ".upool", path)); got != 0o600 {
		t.Errorf("state.json mode = %v, want %v", got, os.FileMode(0o600))
	}
	poolDir := filepath.Join(home, ".upool", "pools", "game-0123abcd")
	if got, want := entries(t, poolDir), []string{"state.json"}; !slices.Equal(got, want) {
		t.Errorf("pool directory holds %q, want %q", got, want)
	}
}

func TestFailedUpdateWritesNothing(t *testing.T) {
	home := t.TempDir()
	if err := Update(context.Background(), home, "registry.json", set("kept")); err != nil {
		t.Fatal(err)
	}
	errFn := errors.New("fn failed")
	err := Update(context.Background(), home, "registry.json", func([]byte) ([]byte, error) {
		return []byte("lost"), errFn
	})
	if err != errFn {
		t.Errorf("Update error = %v, want fn's error as it is", err)
	}
	got, err := Read(home, "registry.json")
	if err != nil || string(got) != "kept" {
		t.Errorf("Read = %q, %v, want %q, nil", got, err, "kept")
	}
	dir := filepath.Join(home, ".upool")
	if got, want := entries(t, dir), []string{"lock", "registry.json"}; !slices.Equal(got, want) {
		t.Errorf("~/.upool holds %q, want %q", got, want)
	}
}

// TestUpdateReleasesTheLock checks that the lock is held only across the read-modify-write
// (docs/contracts.md (e) State schemas), whether fn succeeds or fails.
func TestUpdateReleasesTheLock(t *testing.T) {
	for name, fn := range map[string]func([]byte) ([]byte, error){
		"success": set("x"),
		"failure": func([]byte) ([]byte, error) { return nil, errors.New("fn failed") },
	} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			// Update's error is not what is checked here; the lock is.
			_ = Update(context.Background(), home, "registry.json", fn)
			l, err := Lock(cancelled(), home)
			if err != nil {
				t.Fatalf("Lock after Update error = %v, want nil", err)
			}
			if err := l.Unlock(); err != nil {
				t.Error(err)
			}
		})
	}
}

// TestReaderSeesOnlyWholeFiles reads while another goroutine rewrites the file between two
// documents of a single repeated byte each: a read that mixes them, or catches one half-written,
// means the write was not atomic.
func TestReaderSeesOnlyWholeFiles(t *testing.T) {
	home := t.TempDir()
	docs := [][]byte{bytes.Repeat([]byte("a"), 1<<20), bytes.Repeat([]byte("b"), 1<<20)}
	if err := Update(context.Background(), home, "editors.json", set(string(docs[0]))); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		defer close(done)
		for i := range 50 {
			doc := docs[i%2]
			err := Update(context.Background(), home, "editors.json",
				func([]byte) ([]byte, error) { return doc, nil })
			if err != nil {
				t.Error(err)
				return
			}
		}
	})
	for reads := 0; ; reads++ {
		select {
		case <-done:
			wg.Wait()
			return
		default:
		}
		got, err := Read(home, "editors.json")
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, docs[0]) && !bytes.Equal(got, docs[1]) {
			t.Fatalf("read %d: got %d bytes that are neither whole document", reads, len(got))
		}
	}
}

func TestPathsOutsideTheStateDirectoryAreRefused(t *testing.T) {
	home := t.TempDir()
	for _, path := range []string{"", ".", "lock", "./lock", "..", "../registry.json",
		"pools/../../registry.json", filepath.Join(home, "registry.json")} {
		if _, err := Read(home, path); err == nil {
			t.Errorf("Read(%q) error = nil, want an error", path)
		}
		called := false
		err := Update(context.Background(), home, path, func([]byte) ([]byte, error) {
			called = true
			return nil, nil
		})
		if err == nil || called {
			t.Errorf("Update(%q) = %v with fn called %v, want an error and fn not called",
				path, err, called)
		}
	}
	if names := entries(t, home); len(names) != 0 {
		t.Errorf("home holds %q, want nothing", names)
	}
}

func TestRelativeHomeIsRefused(t *testing.T) {
	for _, home := range []string{"", "home"} {
		if l, err := Lock(context.Background(), home); err == nil {
			_ = l.Unlock() // the error above is the failure
			t.Errorf("Lock(%q) error = nil, want an error", home)
		}
		if _, err := Read(home, "registry.json"); err == nil {
			t.Errorf("Read(%q) error = nil, want an error", home)
		}
		if err := Update(context.Background(), home, "registry.json", set("x")); err == nil {
			t.Errorf("Update(%q) error = nil, want an error", home)
		}
	}
}
