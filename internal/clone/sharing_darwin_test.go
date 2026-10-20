package clone

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// byteCopier copies a file's bytes, as cp does where clonefile(2) cannot share blocks.
type byteCopier struct{}

func (byteCopier) Copy(_ context.Context, src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }() // read-only; the copy's errors are the ones that matter
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close() // the copy's error is the one worth reporting
		return err
	}
	return out.Close()
}

// probeDir is a pool's .claude/worktrees under a temporary main checkout, on the temporary
// directory's volume, which is APFS on macOS.
func probeDir(t *testing.T) (repo, dir string) {
	t.Helper()
	repo, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir = filepath.Join(repo, ".claude", "worktrees")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return repo, dir
}

// checkNoScratch fails the test unless dir is empty again.
func checkNoScratch(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("%s holds %d entries, the first %s, want the scratch files removed", dir,
			len(entries), entries[0].Name())
	}
}

// probeUntil runs ProbeSharing up to three times, until want holds of its result, and returns the
// last result. Other test packages write and free large files on the same volume at the same
// time, which can disturb a measurement however ProbeSharing takes it; three disturbed probes in a
// row would mean the probe, not the volume, is wrong.
func probeUntil(t *testing.T, c Copier, size int64, want func(Sharing) bool) Sharing {
	t.Helper()
	var s Sharing
	for attempt := 1; attempt <= 3; attempt++ {
		repo, dir := probeDir(t)
		var err error
		if s, err = ProbeSharing(t.Context(), c, repo, dir, size); err != nil {
			t.Fatal(err)
		}
		checkNoScratch(t, dir)
		t.Logf("attempt %d: a %d MB copy cost %d bytes of free space", attempt, size>>20, s.Used)
		if want(s) {
			break
		}
	}
	return s
}

func TestProbeSharingFindsThatCPCloneSharesBlocks(t *testing.T) {
	s := probeUntil(t, CP{}, ProbeSize, func(s Sharing) bool { return s.Shares(ProbeSize) })
	if s.FSType != "apfs" || !s.SameDevice || !s.Measured || !s.Shares(ProbeSize) {
		t.Errorf("ProbeSharing = %+v, want APFS, one device, and a copy costing under %d MB",
			s, ProbeSize>>21)
	}
}

// The property doctor checks: a copy that duplicates bytes is caught by the free space it costs.
func TestProbeSharingCatchesAByteCopy(t *testing.T) {
	const size = 64 << 20
	s := probeUntil(t, byteCopier{}, size, func(s Sharing) bool { return !s.Shares(size) })
	if !s.Measured || s.Used < size/2 || s.Shares(size) {
		t.Errorf("ProbeSharing = %+v, want a measured copy costing at least %d MB, not sharing",
			s, size>>21)
	}
}

// failingCopier fails after creating its destination, as an interrupted cp would.
type failingCopier struct{}

func (failingCopier) Copy(_ context.Context, _, dst string) error {
	if err := os.WriteFile(dst, []byte("partial"), 0o644); err != nil {
		return err
	}
	return fmt.Errorf("copy interrupted")
}

func TestProbeSharingRemovesItsScratchFilesWhenTheCopyFails(t *testing.T) {
	repo, dir := probeDir(t)
	if _, err := ProbeSharing(t.Context(), failingCopier{}, repo, dir, 1<<20); err == nil ||
		err.Error() != "copy interrupted" {
		t.Errorf("ProbeSharing error = %v, want the copy's", err)
	}
	checkNoScratch(t, dir)
}
