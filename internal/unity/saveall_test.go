package unity

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"testing"

	"github.com/weslo/upool/internal/output"
	"github.com/weslo/upool/internal/pathvars"
	"github.com/weslo/upool/internal/unity/fake"
)

// saveAllArgs are the arguments every save_all call passes after `unity`.
var saveAllArgs = []string{"command", "save_all", "--format", "json"}

// saveAllIn calls SaveAll on a slot through the fake unity, which answers with replies in order,
// and returns the calls the fake recorded, the slot and SaveAll's error.
func saveAllIn(t *testing.T, replies ...fake.Reply) ([]fake.Call, string, error) {
	t.Helper()
	slot := slotDir(t)
	for i := range replies {
		replies[i].Vars = []pathvars.Var{{Name: "spike", Path: filepath.Dir(slot)}}
	}
	u := fake.Install(t, replies...)
	env := []string{"PATH=" + u.Dir(), "HOME=" + u.Dir()}
	err := Client{Runner: Exec{}, Env: env}.SaveAll(context.Background(), slot)
	return u.Calls(), slot, err
}

// checkSaveAllCalls fails the test unless calls are n save_all calls, each targeting slot.
func checkSaveAllCalls(t *testing.T, calls []fake.Call, n int, slot string) {
	t.Helper()
	if len(calls) != n {
		t.Fatalf("unity called %d times, want %d save_all calls: %+v", len(calls), n, calls)
	}
	for _, c := range calls {
		if !slices.Equal(c.Args, saveAllArgs) || c.ProjectPath != slot || c.Dir != slot {
			t.Errorf("call unity %v with UNITY_PROJECT_PATH %q in %s, want unity %v targeting %s",
				c.Args, c.ProjectPath, c.Dir, saveAllArgs, slot)
		}
	}
}

// The acceptance test of #84: the fake replies to save_all with the first-call timeout, then with
// the save_all capture, and SaveAll succeeds after exactly the re-query A29 decides, save_all once
// more. The timeout's exit was never recorded (docs/contracts.md (d)), so it is replayed with
// several, none of which may change the calls (A2).
func TestSaveAllAfterFirstCallTimeout(t *testing.T) {
	for _, exit := range []int{0, 1, cliExitNoTarget} {
		t.Run(fmt.Sprintf("exit-%d", exit), func(t *testing.T) {
			calls, slot, err := saveAllIn(t,
				fake.Reply{Capture: "command-eval-main-thread-timeout.json", Exit: exit},
				fake.Reply{Capture: "command-save-all.json"})
			if err != nil {
				t.Errorf("SaveAll: %v, want success after the re-query", err)
			}
			checkSaveAllCalls(t, calls, 2, slot)
		})
	}
}

// A save_all that succeeds at once is the only call.
func TestSaveAllThatSucceedsCallsOnce(t *testing.T) {
	calls, slot, err := saveAllIn(t, fake.Reply{Capture: "command-save-all.json"})
	if err != nil {
		t.Errorf("SaveAll: %v", err)
	}
	checkSaveAllCalls(t, calls, 1, slot)
}

// Any failure but the timeout is returned as it is, with no second call: no Editor at the slot
// exits 6 (docs/contracts.md (d); A3).
func TestSaveAllReturnsAnyOtherFailure(t *testing.T) {
	calls, slot, err := saveAllIn(t,
		fake.Reply{Capture: "command-eval-no-pipeline-instance.json", Exit: cliExitNoTarget})
	var f *output.Failure
	if !errors.As(err, &f) || f.Exit() != output.ExitNoTarget ||
		f.Code() != output.CodeCommandFailed || errors.Is(err, ErrMayHaveRun) {
		t.Errorf("SaveAll: %v, want the no-Pipeline-instance failure exiting 6", err)
	}
	checkSaveAllCalls(t, calls, 1, slot)
}

// A second timeout is SaveAll's error, still marked ErrMayHaveRun, and there is no third call.
func TestSaveAllStopsAfterOneReQuery(t *testing.T) {
	timeout := fake.Reply{Capture: "command-eval-main-thread-timeout.json", Exit: 1}
	calls, slot, err := saveAllIn(t, timeout, timeout)
	var f *output.Failure
	if !errors.As(err, &f) || f.Exit() != output.ExitFailure || !errors.Is(err, ErrMayHaveRun) {
		t.Errorf("SaveAll: %v, want the timeout, exiting 1 and marked ErrMayHaveRun", err)
	}
	checkSaveAllCalls(t, calls, 2, slot)
}

// A reply that is not JSON is Call's failure, returned with no second call.
func TestSaveAllReturnsAReplyThatIsNotJSON(t *testing.T) {
	calls, slot, err := saveAllIn(t, fake.Reply{Capture: "version.raw.txt"})
	var f *output.Failure
	if !errors.As(err, &f) || f.Code() != output.CodeCommandFailed || errors.Is(err,
		ErrMayHaveRun) {
		t.Errorf("SaveAll: %v, want the not-JSON failure", err)
	}
	checkSaveAllCalls(t, calls, 1, slot)
}
