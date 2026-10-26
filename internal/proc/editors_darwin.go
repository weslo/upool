package proc

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"github.com/weslo/upool/internal/paths"
)

// editorName is a Unity Editor's process name as the kernel records it, from
// Unity.app/Contents/MacOS/Unity (A45).
const editorName = "Unity"

// projectFlag is the argument before the project's path on a Unity Editor's command line. The Hub
// and the Unity CLI pass it as -projectpath; Unity reads it whatever its case.
const projectFlag = "-projectpath"

// lockFile is the file a Unity Editor creates in its project's Temp/ at launch and holds open while
// it runs (#255).
const lockFile = "Temp/UnityLockfile"

// The sysctl(3) names, as <sys/sysctl.h> numbers them, of every process (CTL_KERN, KERN_PROC,
// KERN_PROC_ALL), of a process's arguments (CTL_KERN, KERN_PROCARGS2) and of their largest size
// (CTL_KERN, KERN_ARGMAX). The syscall package names none of them.
const (
	kernProcAll   = 0
	kernArgMax    = 8
	kernProcArgs2 = 49
)

// pidOffset is where kp_proc.p_pid lies in struct kinfo_proc on 64-bit darwin: after the 16-byte
// p_un, the p_vmspace and p_sigacts pointers, p_flag and p_stat.
const pidOffset = 40

// The proc_info(2) calls, flavors and sizes of <sys/proc_info.h> that list a process's open files:
// PROC_PIDLISTFDS gives each descriptor as a struct proc_fdinfo, and PROC_PIDFDVNODEPATHINFO a
// vnode's as a struct vnode_fdinfowithpath, whose vip_path follows a struct proc_fileinfo and a
// struct vnode_info.
const (
	procInfoCallPIDInfo     = 2
	procInfoCallPIDFDInfo   = 3
	procPIDListFDs          = 1
	procPIDFDVnodePathInfo  = 2
	proxFDTypeVnode         = 1
	procFDInfoSize          = 8
	vnodeFDInfoWithPathSize = 1200
	vnodePathOffset         = 176
)

// The proc_info(2) flavor of <sys/proc_info.h> that gives a process's working directory,
// PROC_PIDVNODEPATHINFO: a struct proc_vnodepathinfo, whose pvi_cdir.vip_path follows a struct
// vnode_info.
const (
	procPIDVnodePathInfo  = 9
	procVnodePathInfoSize = 2352
	cwdPathOffset         = 152
	maxPathLen            = 1024
)

// EditorsAt returns the PIDs of the running Unity Editors that have the checkout at dir open:
// processes the kernel names Unity whose arguments give dir after -projectpath, a relative one
// read from the process's own working directory, or that hold dir/Temp/UnityLockfile open. Both
// show an Editor from its first second, where its Library/EditorInstance.json comes several
// seconds later (A49; #255). Paths compare as paths.Key makes them. A process that exits
// meanwhile, or whose arguments or files cannot be read, is passed over.
func EditorsAt(dir string) ([]int, error) {
	project, err := paths.Key(dir)
	if err != nil {
		return nil, err
	}
	lock, err := paths.Key(filepath.Join(dir, filepath.FromSlash(lockFile)))
	if err != nil {
		return nil, err
	}
	pids, err := pidsNamed(editorName)
	if err != nil {
		return nil, err
	}
	var open []int
	for _, pid := range pids {
		cwd := func() (string, error) { return procCwd(pid) }
		if args, err := procArgs(pid); err == nil && namesProject(args, project, cwd) {
			open = append(open, pid)
			continue
		}
		files, err := vnodePaths(pid)
		if err != nil {
			continue
		}
		for _, f := range files {
			if !strings.EqualFold(filepath.Base(f), filepath.Base(lock)) {
				continue
			}
			if k, err := paths.Key(f); err == nil && k == lock {
				open = append(open, pid)
				break
			}
		}
	}
	return open, nil
}

// namesProject reports whether args give the path whose key is project after -projectpath. A
// relative path is the process's own, from cwd, its working directory, never upool's: a batch run
// started in its project with -projectPath . names that project. One whose working directory
// cannot be read names nothing.
func namesProject(args []string, project string, cwd func() (string, error)) bool {
	for i := 0; i+1 < len(args); i++ {
		if !strings.EqualFold(args[i], projectFlag) {
			continue
		}
		path := args[i+1]
		if !filepath.IsAbs(path) {
			dir, err := cwd()
			if err != nil {
				continue
			}
			// Not filepath.Join, which would clean away a ".." before paths.Key resolves the
			// symlink it follows, as paths.Key itself does.
			path = dir + string(filepath.Separator) + path
		}
		if k, err := paths.Key(path); err == nil && k == project {
			return true
		}
	}
	return false
}

// sysctl runs sysctl(3) on mib into buf and returns how many bytes it wrote, or with buf empty how
// many it would. It makes the system call directly, as kinfoProc does.
func sysctl(mib []int32, buf []byte) (int, syscall.Errno) {
	var p unsafe.Pointer
	if len(buf) > 0 {
		p = unsafe.Pointer(&buf[0])
	}
	size := uintptr(len(buf))
	_, _, errno := syscall.Syscall6(syscall.SYS___SYSCTL, uintptr(unsafe.Pointer(&mib[0])),
		uintptr(len(mib)), uintptr(p), uintptr(unsafe.Pointer(&size)), 0, 0)
	return int(size), errno
}

// pidsNamed returns the PIDs of the running processes the kernel names name, from sysctl(3)'s
// kern.proc.all, read into a buffer with room for processes that start meanwhile.
func pidsNamed(name string) ([]int, error) {
	mib := []int32{ctlKern, kernProc, kernProcAll}
	var buf []byte
	for range 3 {
		need, errno := sysctl(mib, nil)
		if errno != 0 {
			return nil, fmt.Errorf("sysctl kern.proc.all: %w", errno)
		}
		buf = make([]byte, need+need/8)
		n, errno := sysctl(mib, buf)
		if errno == syscall.ENOMEM {
			continue
		}
		if errno != 0 {
			return nil, fmt.Errorf("sysctl kern.proc.all: %w", errno)
		}
		var pids []int
		for off := 0; off+kinfoProcSize <= n; off += kinfoProcSize {
			comm := buf[off+commOffset : off+commOffset+commSize]
			if i := bytes.IndexByte(comm, 0); i >= 0 {
				comm = comm[:i]
			}
			if string(comm) == name {
				pids = append(pids, int(int32(binary.NativeEndian.Uint32(buf[off+pidOffset:]))))
			}
		}
		return pids, nil
	}
	return nil, errors.New("sysctl kern.proc.all: the process table kept growing")
}

// procArgs returns the arguments the process with pid was started with, its argv, from sysctl(3)'s
// kern.procargs2: argc, the executable's path, then argc strings, each ending in a NUL. The error
// wraps ErrNoProcess when no process has the PID.
func procArgs(pid int) ([]string, error) {
	if err := validPID(pid); err != nil {
		return nil, err
	}
	max := make([]byte, 4)
	if _, errno := sysctl([]int32{ctlKern, kernArgMax}, max); errno != 0 {
		return nil, fmt.Errorf("sysctl kern.argmax: %w", errno)
	}
	buf := make([]byte, binary.NativeEndian.Uint32(max))
	n, errno := sysctl([]int32{ctlKern, kernProcArgs2, int32(pid)}, buf)
	switch {
	case errno == syscall.EINVAL || errno == syscall.ESRCH:
		return nil, fmt.Errorf("process %d: %w", pid, ErrNoProcess)
	case errno != 0:
		return nil, fmt.Errorf("sysctl kern.procargs2.%d: %w", pid, errno)
	case n < 4:
		return nil, fmt.Errorf("sysctl kern.procargs2.%d: got %d bytes", pid, n)
	}
	argc := int(int32(binary.NativeEndian.Uint32(buf[:4])))
	rest := buf[4:n]
	// The executable's path, then the NULs that pad it.
	if i := bytes.IndexByte(rest, 0); i >= 0 {
		rest = rest[i:]
	}
	rest = bytes.TrimLeft(rest, "\x00")
	args := make([]string, 0, argc)
	for len(args) < argc && len(rest) > 0 {
		i := bytes.IndexByte(rest, 0)
		if i < 0 {
			args = append(args, string(rest))
			break
		}
		args = append(args, string(rest[:i]))
		rest = rest[i+1:]
	}
	return args, nil
}

// procCwd returns the working directory of the process with pid, from proc_info(2). The error
// wraps ErrNoProcess when no process has the PID.
func procCwd(pid int) (string, error) {
	if err := validPID(pid); err != nil {
		return "", err
	}
	info := make([]byte, procVnodePathInfoSize)
	n, errno := procInfo(procInfoCallPIDInfo, pid, procPIDVnodePathInfo, 0, info)
	switch {
	case errno == syscall.ESRCH:
		return "", fmt.Errorf("process %d: %w", pid, ErrNoProcess)
	case errno != 0:
		return "", fmt.Errorf("reading the working directory of process %d: %w", pid, errno)
	case n != procVnodePathInfoSize:
		return "", fmt.Errorf("reading the working directory of process %d: got %d bytes", pid, n)
	}
	path := info[cwdPathOffset : cwdPathOffset+maxPathLen]
	if i := bytes.IndexByte(path, 0); i >= 0 {
		path = path[:i]
	}
	if len(path) == 0 {
		return "", fmt.Errorf("process %d has no working directory", pid)
	}
	return string(path), nil
}

// procInfo runs proc_info(2), the system call behind libproc's proc_pidinfo and proc_pidfdinfo,
// into buf, and returns how many bytes it wrote, or with buf empty how many it would.
func procInfo(call, pid, flavor int, arg uint64, buf []byte) (int, syscall.Errno) {
	var p unsafe.Pointer
	if len(buf) > 0 {
		p = unsafe.Pointer(&buf[0])
	}
	n, _, errno := syscall.Syscall6(syscall.SYS_PROC_INFO, uintptr(call), uintptr(pid),
		uintptr(flavor), uintptr(arg), uintptr(p), uintptr(len(buf)))
	return int(n), errno
}

// vnodePaths returns the paths of the files, the vnodes, the process with pid holds open, from
// proc_info(2): its descriptors, then each vnode's path. A descriptor closed meanwhile is passed
// over. The error wraps ErrNoProcess when no process has the PID.
func vnodePaths(pid int) ([]string, error) {
	if err := validPID(pid); err != nil {
		return nil, err
	}
	need, errno := procInfo(procInfoCallPIDInfo, pid, procPIDListFDs, 0, nil)
	if errno == syscall.ESRCH {
		return nil, fmt.Errorf("process %d: %w", pid, ErrNoProcess)
	}
	if errno != 0 {
		return nil, fmt.Errorf("listing the files of process %d: %w", pid, errno)
	}
	// Room for descriptors opened meanwhile.
	fds := make([]byte, need+32*procFDInfoSize)
	n, errno := procInfo(procInfoCallPIDInfo, pid, procPIDListFDs, 0, fds)
	if errno != 0 {
		return nil, fmt.Errorf("listing the files of process %d: %w", pid, errno)
	}
	var files []string
	info := make([]byte, vnodeFDInfoWithPathSize)
	for off := 0; off+procFDInfoSize <= n; off += procFDInfoSize {
		fd := int32(binary.NativeEndian.Uint32(fds[off:]))
		if binary.NativeEndian.Uint32(fds[off+4:]) != proxFDTypeVnode {
			continue
		}
		got, errno := procInfo(procInfoCallPIDFDInfo, pid, procPIDFDVnodePathInfo, uint64(fd),
			info)
		if errno != 0 || got != vnodeFDInfoWithPathSize {
			continue
		}
		path := info[vnodePathOffset:]
		if i := bytes.IndexByte(path, 0); i >= 0 {
			path = path[:i]
		}
		if len(path) > 0 {
			files = append(files, string(path))
		}
	}
	return files, nil
}
