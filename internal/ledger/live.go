package ledger

import (
	"errors"
	"fmt"

	"github.com/weslo/upool/internal/paths"
	"github.com/weslo/upool/internal/proc"
)

// LiveIn reports whether the ledger records an Editor with project open, compared by canonical
// path, whose PID is still a process by c. The ledger keeps no start time, so any process at that
// PID counts: a reclaim or a seed choice that waits on it is the safe mistake (docs/design.md,
// "Leases that survive crashes and never reclaim a live tree": nothing is reclaimed while the
// ledger shows a live Editor in the slot; docs/contracts.md (g) step 0).
func (l Ledger) LiveIn(c proc.Checker, project string) (bool, error) {
	key, err := paths.Key(project)
	if err != nil {
		return false, err
	}
	for _, e := range l.Editors {
		ek, err := paths.Key(e.Project)
		if err != nil {
			return false, err
		}
		if ek != key {
			continue
		}
		_, err = c.Start(e.PID)
		if errors.Is(err, proc.ErrNoProcess) {
			continue
		}
		if err != nil {
			return false, fmt.Errorf("checking the Editor of %s, process %d: %w", project, e.PID,
				err)
		}
		return true, nil
	}
	return false, nil
}
