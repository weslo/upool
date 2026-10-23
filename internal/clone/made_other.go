//go:build !darwin

package clone

import (
	"os"
	"time"
)

// Made returns when the directory at dir was last changed, which stands in for when it was made:
// off darwin, upool reads no birth time (A42).
func Made(dir string) (time.Time, error) {
	info, err := os.Lstat(dir)
	if err != nil {
		return time.Time{}, err
	}
	return info.ModTime(), nil
}
