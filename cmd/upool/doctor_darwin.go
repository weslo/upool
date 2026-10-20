package main

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/weslo/upool/internal/clone"
	"github.com/weslo/upool/internal/paths"
)

// cloneSharingCheck checks that a clone made where the slots go shares its blocks: the directory
// is on APFS and on the main checkout's device (spike §1), and a clone of a 128 MB scratch file
// there costs less than half of it in free space (spike §4; docs/design.md, "Why clone the whole
// checkout rather than check out and clone the Library": doctor verifies physical sharing rather
// than assuming it). It runs only on macOS, and reports true that it ran.
func cloneSharingCheck(ctx context.Context, repo string) (doctorCheck, bool) {
	dir := filepath.Join(repo, filepath.FromSlash(".claude/worktrees"))
	s, err := clone.ProbeSharing(ctx, clone.CP{}, repo, dir, clone.ProbeSize)
	mb := func(n int64) int64 { return n >> 20 }
	switch {
	case err != nil:
		return failedCheck(checkCloneSharing, err), true
	case s.FSType != "apfs":
		return doctorCheck{checkCloneSharing, checkFail, fmt.Sprintf(".claude/worktrees is on "+
			"%s, not APFS, so a clone copies every byte", s.FSType)}, true
	case !s.SameDevice:
		return doctorCheck{checkCloneSharing, checkFail, ".claude/worktrees is not on the main " +
			"checkout's device, so a clone cannot share its blocks"}, true
	case !s.Shares(clone.ProbeSize):
		return doctorCheck{checkCloneSharing, checkFail, fmt.Sprintf("a clone of a %d MB file "+
			"cost %d MB of free space: cp -c is copying, not sharing", mb(clone.ProbeSize),
			mb(s.Used))}, true
	}
	return doctorCheck{checkCloneSharing, checkPass, fmt.Sprintf("APFS, one device, and a clone "+
		"of a %d MB file cost %d MB of free space", mb(clone.ProbeSize), mb(max(s.Used, 0)))}, true
}

// networkVolume returns the file system of the volume holding path and whether it is a network
// volume, which statfs does not flag local.
func networkVolume(path string) (fsType string, network bool, err error) {
	v, err := paths.StatVolume(path)
	if err != nil {
		return "", false, err
	}
	return v.FSType, !v.Local, nil
}
