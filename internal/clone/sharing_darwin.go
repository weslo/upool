package clone

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"

	"github.com/weslo/upool/internal/paths"
)

// ProbeSize is the size of the scratch file ProbeSharing clones, 128 MB: large enough that a byte
// copy shows in the volume's free space, which moved by up to 24 MB between the spike's clones of
// a 6.5 GB Library (spike §4).
const ProbeSize = 128 << 20

// probeCopies is how many copies ProbeSharing measures. Anything else writing to or freeing space
// on the volume during a copy, an Editor importing or another probe, moves the free space too, so
// one disturbed measurement must not decide: the median of three needs two disturbed alike. The
// copies are all removed at the end, since APFS can return a removed file's space later, during
// the next measurement.
const probeCopies = 3

// Sharing is what ProbeSharing found about copies made in a pool's .claude/worktrees.
type Sharing struct {
	// FSType is the directory's file system; clonefile(2) needs APFS.
	FSType string
	// SameDevice is whether the directory is on the main checkout's device: a clone shares blocks
	// with the checkout only within one (spike §1).
	SameDevice bool
	// Measured is whether the copies were made: only on APFS and the checkout's device. Used is
	// then the median of how many bytes of free space each cost, which may be negative when
	// something else freed space meanwhile.
	Measured bool
	Used     int64
}

// Shares reports whether copies share blocks: APFS, the checkout's device, and a copy of a
// size-byte file that cost less than half of size in free space.
func (s Sharing) Shares(size int64) bool {
	return s.FSType == "apfs" && s.SameDevice && s.Measured && s.Used < size/2
}

// ProbeSharing checks that copies c makes in dir, a pool's .claude/worktrees, share blocks rather
// than duplicate them, which doctor verifies rather than assumes (docs/design.md, "Why clone the
// whole checkout rather than check out and clone the Library"). It reads dir's file system and
// device and repo's device; then, on APFS and one device, it writes a size-byte scratch file in
// dir, copies it with c, and measures the volume's free space before and after the copy, the
// spike's free-space method (spike §4), probeCopies times. It removes every scratch file before it
// returns.
func ProbeSharing(ctx context.Context, c Copier, repo, dir string, size int64) (s Sharing,
	err error) {
	vol, err := paths.StatVolume(dir)
	if err != nil {
		return Sharing{}, err
	}
	checkout, err := paths.StatVolume(repo)
	if err != nil {
		return Sharing{}, err
	}
	s = Sharing{FSType: vol.FSType, SameDevice: vol.Device == checkout.Device}
	if s.FSType != "apfs" || !s.SameDevice {
		return s, nil
	}

	src, err := writeScratch(dir, size)
	if src != "" {
		defer func() { err = errors.Join(err, removeScratch(src)) }()
	}
	if err != nil {
		return Sharing{}, err
	}
	used := make([]int64, 0, probeCopies)
	for i := range probeCopies {
		dst := fmt.Sprintf("%s-copy%d", src, i+1)
		defer func() { err = errors.Join(err, removeScratch(dst)) }()
		u, err := measureCopy(ctx, c, src, dst, dir)
		if err != nil {
			return Sharing{}, err
		}
		used = append(used, u)
	}
	slices.Sort(used)
	s.Measured, s.Used = true, used[len(used)/2]
	return s, nil
}

// measureCopy copies src to dst with c and returns how many bytes of dir's volume's free space the
// copy cost, once it is on disk.
func measureCopy(ctx context.Context, c Copier, src, dst, dir string) (int64, error) {
	before, err := paths.StatVolume(dir)
	if err != nil {
		return 0, err
	}
	if err := c.Copy(ctx, src, dst); err != nil {
		return 0, err
	}
	// A copy that fell back to writing bytes counts once they are on disk.
	if err := syncFile(dst); err != nil {
		return 0, err
	}
	after, err := paths.StatVolume(dir)
	if err != nil {
		return 0, err
	}
	return int64(before.Free) - int64(after.Free), nil
}

// writeScratch writes a size-byte file in dir, synced to disk, and returns its path, which is set
// whenever the file was created, even if writing it then failed.
func writeScratch(dir string, size int64) (path string, err error) {
	f, err := os.CreateTemp(dir, ".upool-doctor-*")
	if err != nil {
		return "", fmt.Errorf("creating a scratch file in %s: %w", dir, err)
	}
	path = f.Name()
	chunk := make([]byte, 1<<20)
	for i := range chunk {
		chunk[i] = byte(i)
	}
	for left := size; left > 0 && err == nil; left -= int64(len(chunk)) {
		_, err = f.Write(chunk[:min(left, int64(len(chunk)))])
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return path, fmt.Errorf("writing %s: %w", path, err)
	}
	return path, nil
}

// syncFile flushes path's data to disk.
func syncFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("opening %s: %w", path, err)
	}
	err = f.Sync()
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("syncing %s: %w", path, err)
	}
	return nil
}

// removeScratch removes a scratch file, which a failed copy may never have made.
func removeScratch(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("removing %s: %w", path, err)
	}
	return nil
}
