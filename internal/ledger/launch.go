package ledger

import (
	"context"
	"fmt"

	"github.com/weslo/upool/internal/paths"
)

// Launched replaces the PID of the Editor recorded for project under admitted, the process that
// opened it, with launched, the Editor's own, under ~/.upool/lock. `upool open` admits an Editor
// before it launches it, under its own PID, so that a refused admission launches nothing (A38)
// and an open that dies before the Editor is up leaves an entry the next ledger call drops as
// dead (docs/contracts.md (e), editors[].pid). It fails with ErrNotInLedger when no such entry is
// there.
func Launched(ctx context.Context, home, project string, admitted, launched int) error {
	return Update(ctx, home, func(l *Ledger) error {
		i, err := l.find(project, admitted)
		if err != nil {
			return err
		}
		if i < 0 {
			return fmt.Errorf("recording the Editor of %s, process %d: %w", project, launched,
				ErrNotInLedger)
		}
		l.Editors[i].PID = launched
		return nil
	})
}

// Withdraw removes the Editor recorded for project under pid, and no other, under ~/.upool/lock:
// what a failed `upool open` undoes, which must not take another Editor of the same project with
// it. It reports whether the entry was there.
func Withdraw(ctx context.Context, home, project string, pid int) (bool, error) {
	found := false
	err := Update(ctx, home, func(l *Ledger) error {
		i, err := l.find(project, pid)
		if err != nil || i < 0 {
			return err
		}
		found = true
		l.Editors = append(l.Editors[:i], l.Editors[i+1:]...)
		return nil
	})
	return found, err
}

// find returns the index of the Editor recorded for project, compared by canonical path, under
// pid, or -1.
func (l Ledger) find(project string, pid int) (int, error) {
	key, err := paths.Key(project)
	if err != nil {
		return -1, err
	}
	for i, e := range l.Editors {
		ek, err := paths.Key(e.Project)
		if err != nil {
			return -1, err
		}
		if ek == key && e.PID == pid {
			return i, nil
		}
	}
	return -1, nil
}
