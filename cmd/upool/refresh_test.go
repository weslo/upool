package main

import (
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// The session-start hook's starter runs `upool refresh` detached: a script stands in for upool,
// in a directory of its own, and says through a FIFO when it has written, so that the test waits
// for it without sleeping. It runs in the pool's main checkout, as the leader of a process group
// of its own, with stdin at end of file, and its output goes to refresh.log, after what was there.
func TestStartRefreshRunsRefreshDetached(t *testing.T) {
	home, checkout := tempDir(t), tempDir(t)
	key := "down-3f9a1c2e"
	log := filepath.Join(home, ".upool", "pools", key, refreshLog)
	if err := os.MkdirAll(filepath.Dir(log), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(log, []byte("an earlier refresh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(tempDir(t), "written")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	upool := filepath.Join(tempDir(t), "upool")
	script := "#!/bin/sh\n" +
		"echo \"args: $*\"\n" +
		"echo \"cwd: $(pwd -P)\"\n" +
		"if read -r line; then echo \"stdin: $line\"; else echo 'stdin: at end'; fi\n" +
		"[ \"$(ps -o pgid= -p $$ | tr -d ' ')\" = \"$$\" ] && echo 'own process group'\n" +
		"echo written > \"$UPOOL_TEST_FIFO\"\n"
	if err := os.WriteFile(upool, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	inv := &invocation{home: home, executable: func() (string, error) { return upool, nil },
		environ: func() []string {
			return []string{"PATH=/usr/bin:/bin", "UPOOL_TEST_FIFO=" + fifo}
		}}

	// The test's end of the FIFO is open, read and write, before the script starts: such an open
	// never blocks, and the script's open for writing then finds a reader at once. Opening it only
	// after the script had written and exited could leave the reader waiting in open(2) for a
	// writer that had gone (#195).
	reader, err := os.OpenFile(fifo, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }() // read-only use; closing loses nothing

	if err := startRefresh(inv, checkout, key); err != nil {
		t.Fatalf("startRefresh: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		// Blocks until the script writes its line; the test's own write end means no end of file.
		_, err := io.ReadFull(reader, make([]byte, len("written\n")))
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the detached refresh never wrote")
	}
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(checkout)
	if err != nil {
		t.Fatal(err)
	}
	want := "an earlier refresh\nargs: refresh\ncwd: " + resolved + "\nstdin: at end\n" +
		"own process group\n"
	if got := string(b); got != want {
		t.Errorf("refresh.log = %q, want %q", got, want)
	}
}
