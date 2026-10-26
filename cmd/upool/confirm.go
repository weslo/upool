package main

import (
	"context"
	"fmt"

	"github.com/weslo/upool/internal/hooks"
)

// confirmReport is what take --confirm did: the data of its --json envelope, whose human form is
// the slot's path alone, as take's is.
type confirmReport struct {
	Path    string `json:"path"`
	Slot    string `json:"slot"`
	LeaseID string `json:"lease_id"`
	Repo    string `json:"repo"`
	// Reset says whether the confirm ran the slot's deferred reset (A23).
	Reset bool `json:"reset"`
}

// Human returns the slot's path.
func (r confirmReport) Human() string { return r.Path }

// confirm is `upool take --confirm <path>`, which SessionStart runs for the session's slot
// (docs/design.md, "Environment, confirmation and release"; docs/contracts.md (b); (f) transition
// 6): internal/hooks' Confirm, the one the session-start hook runs in-process, with this process's
// parent, the session's CLI, as the lease's holder (A6). otherFlags is whether a flag of take's
// other form was given, which --confirm refuses.
func confirm(inv *invocation, path string, otherFlags bool) (any, []string, error) {
	if len(inv.args) > 0 || otherFlags {
		return nil, nil, fmt.Errorf("take --confirm takes a path alone; usage: upool %s",
			takeUsage)
	}
	c, warnings, err := hooks.Confirm(context.Background(), hooksEnv(inv), path, "")
	if err != nil {
		return nil, warnings, err
	}
	return confirmReport{Path: c.Path, Slot: c.Slot, LeaseID: c.LeaseID, Repo: c.Repo,
		Reset: c.Reset}, warnings, nil
}
