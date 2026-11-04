package unity

import (
	"context"
)

// SaveAll saves every open scene of the Editor in the slot at slot: `unity command save_all
// --format json` through the verb table (testdata/captures/unity/command-save-all.json). `upool
// close` runs it before `unity close`, which discards unsaved scenes without asking
// (docs/design.md, "Editors: budget by RAM, serialize cold imports as a courtesy, target by
// variable and by path").
//
// When the call fails with the main-thread timeout, whose code may have run anyway
// (ErrMayHaveRun; spike §8 C.3), SaveAll re-queries by running save_all once more: saving scenes
// that are already saved changes nothing, so the second call is the re-query (A29). The second
// call's error is SaveAll's, and a second timeout is a TimedOutError. Each call is bounded by
// SaveTimeout, and one the bound cuts is a TimedOutError with no second call: a save that does
// not answer is held by something, likely a native panel, that a second save would wait on too
// (#280). Any other failure is returned as it is, without a second call.
func (c Client) SaveAll(ctx context.Context, slot string) error {
	return requeried(func() error { return c.saveAll(ctx, slot) })
}

// saveAll runs save_all once, bounded by SaveTimeout, and returns the error upool fails with for
// its reply, if any.
func (c Client) saveAll(ctx context.Context, slot string) error {
	return bounded(ctx, "unity command save_all "+slot, func(ctx context.Context) error {
		reply, err := c.Call(ctx, VerbCommand, slot, "save_all")
		if err != nil {
			return err
		}
		return reply.Err()
	})
}
