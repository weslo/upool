package clone

import (
	"context"
	"fmt"

	"github.com/weslo/upool/internal/unity"
)

// KeyCheck compares a slot's `unity cache key` before and after a checkout upool makes in it:
// the key hashes the Editor version, packages-lock.json and the build target, so a change means
// the slot's next open is a large reimport, which the slot records as reimport_pending and from M3
// `upool open` charges as a cold import (docs/contracts.md (f), the paragraph on checkouts; (g)
// step 7; A20). The clone recipe's step 7 and release's reset use it.
type KeyCheck struct {
	// Env is the environment unity runs with; its PATH is where unity is found.
	Env []string
}

// Around runs checkout in the slot at dir between two `unity cache key "<dir>"` calls and reports
// whether the key changed. Without unity on PATH it runs checkout alone and reports no change,
// since doctor reports a missing Unity CLI. When a key call fails it still runs checkout and
// reports no change, with note saying why for the caller to warn with. err is checkout's.
func (k KeyCheck) Around(ctx context.Context, dir string,
	checkout func() error) (changed bool, note string, err error) {
	before, found, keyErr := unity.CacheKey(ctx, k.Env, dir)
	if err := checkout(); err != nil {
		return false, "", err
	}
	if !found {
		return false, "", nil
	}
	if keyErr != nil {
		return false, notCompared(dir, keyErr), nil
	}
	after, _, keyErr := unity.CacheKey(ctx, k.Env, dir)
	if keyErr != nil {
		return false, notCompared(dir, keyErr), nil
	}
	return before != after, "", nil
}

// notCompared is the note for a key that could not be read.
func notCompared(dir string, err error) string {
	return fmt.Sprintf("the cache key of %s was not compared across its checkout, so "+
		"reimport_pending is left as it was: %v", dir, err)
}

// ReimportWarning is the warning for a slot whose key changed across a checkout.
func ReimportWarning(slot string) string {
	return slot + "'s unity cache key changed across its checkout: its next open is a large " +
		"reimport"
}
