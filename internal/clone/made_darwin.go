package clone

import (
	"fmt"
	"syscall"
	"time"
)

// Made returns when the directory at dir was made: its birth time, which APFS keeps. prune counts a
// slot with no last_used, cloned before the recipe recorded one, as idle since then (A42).
func Made(dir string) (time.Time, error) {
	var st syscall.Stat_t
	if err := syscall.Lstat(dir, &st); err != nil {
		return time.Time{}, fmt.Errorf("reading the birth time of %s: %w", dir, err)
	}
	return time.Unix(st.Birthtimespec.Unix()), nil
}
