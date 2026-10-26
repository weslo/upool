package proc

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"syscall"
	"time"
	"unsafe"
)

// The sysctl(3) name kern.proc.pid.<pid>, as <sys/sysctl.h> numbers it: CTL_KERN, KERN_PROC,
// KERN_PROC_PID. The syscall package names none of them.
const (
	ctlKern     = 1
	kernProc    = 14
	kernProcPID = 1
)

// kinfoProcSize is sizeof(struct kinfo_proc) from <sys/sysctl.h> on 64-bit darwin, arm64 and amd64
// alike. Its first field, kp_proc.p_un.__p_starttime, is a struct timeval: tv_sec as an int64,
// then tv_usec as an int32.
const kinfoProcSize = 648

// commOffset and commSize are where kp_proc.p_comm, the process's name as the kernel records it,
// the executable's base name cut to MAXCOMLEN bytes and ending in a NUL, lies in struct
// kinfo_proc on 64-bit darwin: 243 on a sleep, a test binary and launchd alike. A running Unity
// Editor's read "Unity", from Unity.app/Contents/MacOS/Unity (#243; A45).
const (
	commOffset = 243
	commSize   = 17
)

// Start returns the start time of the process with pid, to the microsecond and in UTC, from
// sysctl(3)'s kern.proc.pid. The error wraps ErrNoProcess when no process has the PID.
func Start(pid int) (time.Time, error) {
	info, err := kinfoProc(pid)
	if err != nil {
		return time.Time{}, err
	}
	sec := int64(binary.NativeEndian.Uint64(info[0:8]))
	usec := int64(int32(binary.NativeEndian.Uint32(info[8:12])))
	return time.Unix(sec, usec*int64(time.Microsecond)).UTC(), nil
}

// Name returns the name of the process with pid as the kernel records it, its executable's base
// name cut to 16 bytes, from sysctl(3)'s kern.proc.pid. The error wraps ErrNoProcess when no
// process has the PID.
func Name(pid int) (string, error) {
	info, err := kinfoProc(pid)
	if err != nil {
		return "", err
	}
	comm := info[commOffset : commOffset+commSize]
	if i := bytes.IndexByte(comm, 0); i >= 0 {
		comm = comm[:i]
	}
	return string(comm), nil
}

// kinfoProc returns the struct kinfo_proc of the process with pid, from sysctl(3)'s
// kern.proc.pid. The error wraps ErrNoProcess when no process has the PID.
//
// It makes the system call directly, because the syscall package wraps libc's sysctl(3) without
// exporting it, and golang.org/x/sys, which does export it, would be a new dependency
// (CONTRIBUTING.md, rule 5). A reply of any other size than kinfoProcSize is an error rather than
// a guess at a changed layout.
func kinfoProc(pid int) ([kinfoProcSize]byte, error) {
	var info [kinfoProcSize]byte
	if err := validPID(pid); err != nil {
		return info, err
	}
	mib := [4]int32{ctlKern, kernProc, kernProcPID, int32(pid)}
	size := uintptr(len(info))
	_, _, errno := syscall.Syscall6(syscall.SYS___SYSCTL,
		uintptr(unsafe.Pointer(&mib[0])), uintptr(len(mib)),
		uintptr(unsafe.Pointer(&info[0])), uintptr(unsafe.Pointer(&size)), 0, 0)
	if errno != 0 {
		return info, fmt.Errorf("sysctl kern.proc.pid.%d: %w", pid, errno)
	}
	// The kernel answers a PID nobody holds with no error and nothing written.
	if size == 0 {
		return info, fmt.Errorf("process %d: %w", pid, ErrNoProcess)
	}
	if size != kinfoProcSize {
		return info, fmt.Errorf("sysctl kern.proc.pid.%d: got %d bytes, want %d", pid, size,
			kinfoProcSize)
	}
	return info, nil
}
