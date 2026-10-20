package paths

import (
	"fmt"
	"os"
	"syscall"
)

// mntLocal is MNT_LOCAL from <sys/mount.h>, the statfs flag of a volume on local storage, which
// the syscall package does not name.
const mntLocal = 0x00001000

// Volume is what statfs(2) and stat(2) report about the volume holding a path, for doctor's checks
// of where slots and state live (docs/design.md, "One binary, a dozen commands, one JSON file per
// pool", the `doctor` row and the state placement paragraph).
type Volume struct {
	// FSType is the file system's name, such as "apfs" or "smbfs".
	FSType string
	// Local is false for a network volume, on which statfs leaves MNT_LOCAL off.
	Local bool
	// Device is the path's st_dev: two paths can share blocks through clonefile(2) only on one
	// device.
	Device int32
	// Free is the space, in bytes, available to an unprivileged process: f_bavail × f_bsize.
	Free uint64
}

// StatVolume returns the Volume holding path, which must exist; a symlink is followed.
func StatVolume(path string) (Volume, error) {
	var s syscall.Statfs_t
	if err := syscall.Statfs(path, &s); err != nil {
		return Volume{}, fmt.Errorf("statfs %s: %w", path, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return Volume{}, fmt.Errorf("checking %s: %w", path, err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return Volume{}, fmt.Errorf("checking %s: no stat(2) result", path)
	}
	var name []byte
	for _, c := range s.Fstypename {
		if c == 0 {
			break
		}
		name = append(name, byte(c))
	}
	return Volume{FSType: string(name), Local: s.Flags&mntLocal != 0, Device: st.Dev,
		Free: s.Bavail * uint64(s.Bsize)}, nil
}
