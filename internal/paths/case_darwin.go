package paths

import (
	"errors"
	"fmt"
	"syscall"
)

// pcCaseSensitive is _PC_CASE_SENSITIVE from <sys/unistd.h>, which the syscall package does not
// name.
const pcCaseSensitive = 11

// CaseSensitive reports whether the volume holding path, which must exist, is case-sensitive, by
// asking pathconf(2) for _PC_CASE_SENSITIVE. A volume that does not answer (EINVAL) counts as
// case-sensitive, as every volume does off darwin.
func CaseSensitive(path string) (bool, error) {
	v, err := syscall.Pathconf(path, pcCaseSensitive)
	if errors.Is(err, syscall.EINVAL) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("pathconf %s: %w", path, err)
	}
	return v != 0, nil
}
