// Package proc says whether a process recorded by PID and start time is still that process: the
// liveness behind a lease's holder and a slot's owner (docs/design.md, "Leases that survive
// crashes and never reclaim a live tree").
//
// A PID alone is not enough, because the kernel reuses PIDs: a holder that died and a stranger that
// later got its PID would look the same, and the stranger would keep a dead lease alive forever. So
// upool records a process's start time beside its PID (holder_pid_start and owner_pid_start in
// docs/contracts.md (e) State schemas) and counts the process alive only while both still match.
// Start times are the ones Start reads, to the microsecond, and are compared at that precision,
// never against another source such as `ps -o lstart` (docs/contracts.md, A7).
package proc

import (
	"errors"
	"fmt"
	"math"
	"time"
)

// ErrNoProcess is the error Start wraps when no process has the PID.
var ErrNoProcess = errors.New("no such process")

// Alive reports whether the process with pid is running and started at start exactly, so a PID the
// kernel has given to another process reads as dead. An error means liveness is unknown, such as
// on a platform Start does not support; a caller must not treat it as dead, or it would reclaim a
// live tree.
func Alive(pid int, start time.Time) (bool, error) {
	got, err := Start(pid)
	if errors.Is(err, ErrNoProcess) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return got.Equal(start), nil
}

// validPID returns an error unless pid can name a user process: the kernel numbers them from 1, and
// a pid_t is 32 bits.
func validPID(pid int) error {
	if pid < 1 || pid > math.MaxInt32 {
		return fmt.Errorf("invalid pid %d", pid)
	}
	return nil
}

// Checker is Start, Alive, Name and EditorsAt behind an interface, so that pool and ledger tests
// inject liveness instead of spawning processes. Start is in it too, because Start fails off darwin
// and those packages' tests also run on Linux in CI. System is the real one.
type Checker interface {
	Start(pid int) (time.Time, error)
	Alive(pid int, start time.Time) (bool, error)
	Name(pid int) (string, error)
	EditorsAt(dir string) ([]int, error)
}

// System is the Checker that asks the operating system.
type System struct{}

var _ Checker = System{}

// Start returns Start(pid).
func (System) Start(pid int) (time.Time, error) {
	return Start(pid)
}

// Alive returns Alive(pid, start).
func (System) Alive(pid int, start time.Time) (bool, error) {
	return Alive(pid, start)
}

// Name returns Name(pid).
func (System) Name(pid int) (string, error) {
	return Name(pid)
}

// EditorsAt returns EditorsAt(dir).
func (System) EditorsAt(dir string) ([]int, error) {
	return EditorsAt(dir)
}
