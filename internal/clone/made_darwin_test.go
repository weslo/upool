package clone

import (
	"os"
	"testing"
	"time"
)

// Made reads a directory's birth time. Setting a modification time earlier than the birth time
// moves the birth time back with it on APFS, which is how the test gives a directory an old one.
func TestMadeReadsTheBirthTime(t *testing.T) {
	dir := t.TempDir()
	before := time.Now().Add(-time.Second)
	got, err := Made(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Before(before) {
		t.Errorf("Made = %s for a new directory, want after %s", got, before)
	}
	old := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	if err := os.Chtimes(dir, old, old); err != nil {
		t.Fatal(err)
	}
	if got, err := Made(dir); err != nil || !got.Equal(old) {
		t.Errorf("Made = %s, %v after the times moved to %s, want %s", got, err, old, old)
	}
}
