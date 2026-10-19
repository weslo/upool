// Package statefile is the one way upool reads and writes its state files: the per-pool
// state.json, registry.json and editors.json under ~/.upool (docs/contracts.md (e) State schemas).
//
// All of ~/.upool sits under one lock, ~/.upool/lock (docs/design.md, "Repository, CI and Homebrew
// distribution", the second of its five rules). `take` reads the Editor ledger and `release` writes
// both a pool's state and the ledger, so a lock per file would need a fixed order to avoid
// deadlock, while a handful of agents doing millisecond rewrites never contend for one lock in
// practice (docs/design.md, "One binary, a dozen commands, one JSON file per pool"). The lock is
// held only across a read-modify-write, never across a 26 s clone or a 235 s import.
//
// Every write goes to a temporary file in the same directory, is synced, and is renamed over the
// old file, so a reader sees the old document or the new one and never half of either. That is why
// Read takes no lock.
//
// The home directory is always a parameter; nothing here reads the environment (CONTRIBUTING.md,
// "Process state comes in through run").
package statefile

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

const (
	// dirName is upool's state directory, under the home directory.
	dirName = ".upool"
	// lockName is the lock file, in dirName. It is never a state file: renaming a new file over it
	// would hand later lockers a different file to lock.
	lockName = "lock"
)

// Locked is a held ~/.upool/lock, released by Unlock.
type Locked struct {
	f *os.File
}

// Lock takes ~/.upool/lock under home, creating ~/.upool with mode 0700 if it is missing, and
// waits for as long as another process, or another Lock in this one, holds it. When ctx ends first,
// Lock gives up and returns an error wrapping ctx.Err(); a lock that is free is taken even if ctx
// has already ended.
//
// It is flock(2) on the file, so the lock belongs to the open file, not to the process: a second
// Lock in a process that holds the lock waits like any other, and Lock must not be called while the
// caller already holds it. The file is close-on-exec, so a command upool runs never inherits it.
func Lock(ctx context.Context, home string) (*Locked, error) {
	if err := checkHome(home); err != nil {
		return nil, err
	}
	dir := filepath.Join(home, dirName)
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, fmt.Errorf("creating %s: %w", dir, err)
	}
	path := filepath.Join(dir, lockName)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}
	fd := int(f.Fd())

	err = flock(fd, syscall.LOCK_EX|syscall.LOCK_NB)
	if err == nil {
		return &Locked{f: f}, nil
	}
	if !errors.Is(err, syscall.EWOULDBLOCK) {
		_ = f.Close() // the flock error is the one worth reporting
		return nil, fmt.Errorf("locking %s: %w", path, err)
	}

	// A blocking flock cannot be interrupted, so it runs on its own goroutine while this one also
	// watches ctx. If Lock gives up first, the flock still finishes when the holder lets go, and
	// closing the file then releases whatever it got, so giving up leaves no lock behind.
	done := make(chan error)
	abandoned := make(chan struct{})
	go func() {
		err := flock(fd, syscall.LOCK_EX)
		select {
		case done <- err:
		case <-abandoned:
			_ = f.Close() // nobody is left to report a failure to
		}
	}()
	select {
	case err := <-done:
		if err != nil {
			_ = f.Close() // the flock error is the one worth reporting
			return nil, fmt.Errorf("locking %s: %w", path, err)
		}
		return &Locked{f: f}, nil
	case <-ctx.Done():
		close(abandoned)
		return nil, fmt.Errorf("waiting for %s: %w", path, ctx.Err())
	}
}

// Unlock releases the lock by closing the lock file, which is the only descriptor that holds it.
func (l *Locked) Unlock() error {
	if err := l.f.Close(); err != nil {
		return fmt.Errorf("unlocking %s: %w", l.f.Name(), err)
	}
	return nil
}

// flock is flock(2), retried when a signal interrupts the wait.
func flock(fd, how int) error {
	for {
		err := syscall.Flock(fd, how)
		if !errors.Is(err, syscall.EINTR) {
			return err
		}
	}
}

// Update rewrites the state file at path, which is relative to ~/.upool under home, under the
// lock: it takes the lock, reads the file (a missing file reads as empty), passes the contents to
// fn, writes what fn returns to a temporary file in the same directory, syncs it, renames it over
// the file, and releases the lock. Directories on the way to the file are created with mode 0700,
// and the file has mode 0600.
//
// When fn returns an error, Update returns it as it is and writes nothing. Waiting for the lock
// gives up when ctx ends, as Lock does.
func Update(ctx context.Context, home, path string,
	fn func(old []byte) ([]byte, error)) (err error) {
	file, err := stateFile(home, path)
	if err != nil {
		return err
	}
	l, err := Lock(ctx, home)
	if err != nil {
		return err
	}
	defer func() {
		if uerr := l.Unlock(); uerr != nil && err == nil {
			err = uerr
		}
	}()
	old, err := read(file)
	if err != nil {
		return err
	}
	data, err := fn(old)
	if err != nil {
		return err
	}
	return write(file, data)
}

// Read returns the contents of the state file at path, which is relative to ~/.upool under home; a
// missing file reads as empty. It takes no lock and creates nothing, because every write replaces
// a file whole, by rename. A caller that needs several files to agree takes Lock around its reads.
func Read(home, path string) ([]byte, error) {
	file, err := stateFile(home, path)
	if err != nil {
		return nil, err
	}
	return read(file)
}

// checkHome refuses a home directory that is not absolute, so that an empty or relative one is not
// quietly taken as a directory under the working directory.
func checkHome(home string) error {
	if !filepath.IsAbs(home) {
		return fmt.Errorf("home directory %q is not an absolute path", home)
	}
	return nil
}

// stateFile returns the state file at path under home, or an error unless path names a file inside
// ~/.upool other than the lock, which every state file must be to fall under the lock.
func stateFile(home, path string) (string, error) {
	if err := checkHome(home); err != nil {
		return "", err
	}
	clean := filepath.Clean(path)
	if !filepath.IsLocal(path) || clean == "." || clean == lockName {
		return "", fmt.Errorf("state file %q is not a file inside %s", path,
			filepath.Join(home, dirName))
	}
	return filepath.Join(home, dirName, clean), nil
}

func read(file string) ([]byte, error) {
	data, err := os.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", file, err)
	}
	return data, nil
}

// write replaces file with data atomically: a temporary file beside it, synced, then renamed over
// it. A failure removes the temporary file and leaves file as it was.
func write(file string, data []byte) (err error) {
	dir := filepath.Dir(file)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(file)+".tmp*")
	if err != nil {
		return fmt.Errorf("writing %s: %w", file, err)
	}
	defer func() {
		if err != nil {
			// Close fails harmlessly if the file is already closed, and a temporary file that
			// cannot be removed is no reason to hide the error that got here.
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("writing %s: %w", tmp.Name(), err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("syncing %s: %w", tmp.Name(), err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", tmp.Name(), err)
	}
	if err := os.Rename(tmp.Name(), file); err != nil {
		return fmt.Errorf("renaming %s to %s: %w", tmp.Name(), file, err)
	}
	return nil
}
