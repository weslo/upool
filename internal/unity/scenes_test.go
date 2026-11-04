package unity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/weslo/upool/internal/output"
	"github.com/weslo/upool/internal/pathvars"
	"github.com/weslo/upool/internal/unity/fake"
)

// The tests of the scene listing and the one-scene save `upool close` runs before it closes an
// Editor (#280), through the fake unity. Neither reply has been captured from a real Editor yet
// (testdata/README.md, the pending rows), so the fake replays the captured reply of an eval,
// testdata/captures/unity/command-eval-first-call.json, with the string upool's code returns in
// place of the one that eval's code returned: the envelope is the CLI's, the value upool's own.

// evalReply is the captured eval reply with returned as the string its code returned.
func evalReply(t *testing.T, returned string) fake.Reply {
	t.Helper()
	quoted, err := json.Marshal(returned)
	if err != nil {
		t.Fatal(err)
	}
	return fake.Reply{Capture: "command-eval-first-call.json",
		Replace: []string{`"result": "6000.4.11f1"`, `"result": ` + string(quoted)}}
}

// mainThreadTimeout is the first-call timeout of an eval on a freshly opened GUI Editor, whose
// code may have run anyway (spike §8 C.3). Its exit was never recorded (docs/contracts.md (d)).
var mainThreadTimeout = fake.Reply{Capture: "command-eval-main-thread-timeout.json", Exit: 1}

// evalIn runs call against a slot through the fake unity, which answers with replies in order, and
// returns the calls the fake recorded, the slot and call's error.
func evalIn(t *testing.T, call func(c Client, slot string) error,
	replies ...fake.Reply) ([]fake.Call, string, error) {
	t.Helper()
	slot := slotDir(t)
	for i := range replies {
		replies[i].Vars = []pathvars.Var{{Name: "repo", Path: filepath.Dir(slot)},
			{Name: "spike", Path: filepath.Dir(slot)}}
	}
	u := fake.Install(t, replies...)
	env := []string{"PATH=" + u.Dir(), "HOME=" + u.Dir()}
	err := call(Client{Runner: Exec{}, Env: env}, slot)
	return u.Calls(), slot, err
}

// checkEvalCalls fails the test unless calls are n evals of code, each targeting slot.
func checkEvalCalls(t *testing.T, calls []fake.Call, n int, slot, code string) {
	t.Helper()
	want := []string{"command", "eval", code, "--format", "json"}
	if len(calls) != n {
		t.Fatalf("unity called %d times, want %d evals: %+v", len(calls), n, calls)
	}
	for _, c := range calls {
		if !slices.Equal(c.Args, want) || c.ProjectPath != slot || c.Dir != slot {
			t.Errorf("call unity %q with UNITY_PROJECT_PATH %q in %s, want unity %q targeting %s",
				c.Args, c.ProjectPath, c.Dir, want, slot)
		}
	}
}

// ListScenes reads one scene a line, in the Editor's order: its path, empty for an Untitled scene,
// and whether it has unsaved changes. The listing is one eval of listScenesCode.
func TestListScenesReadsEachScenesPathAndChanges(t *testing.T) {
	var scenes []OpenScene
	calls, slot, err := evalIn(t, func(c Client, slot string) error {
		var err error
		scenes, err = c.ListScenes(context.Background(), slot)
		return err
	}, evalReply(t, "1\tAssets/Down/States/Startup/Startup.unity\n0\t\n1\tAssets/B c.unity\n"))
	if err != nil {
		t.Fatalf("ListScenes: %v", err)
	}
	want := []OpenScene{{Path: "Assets/Down/States/Startup/Startup.unity", Dirty: true},
		{Path: "", Dirty: false}, {Path: "Assets/B c.unity", Dirty: true}}
	if !slices.Equal(scenes, want) {
		t.Errorf("ListScenes = %+v, want %+v", scenes, want)
	}
	checkEvalCalls(t, calls, 1, slot, listScenesCode)
}

// A listing that meets the main-thread timeout is asked once more, since it changes nothing (A29);
// a second timeout is a TimedOutError, still marked ErrMayHaveRun, with no third call.
func TestListScenesReQueriesOnceAfterAMainThreadTimeout(t *testing.T) {
	list := func(c Client, slot string) error {
		_, err := c.ListScenes(context.Background(), slot)
		return err
	}
	calls, slot, err := evalIn(t, list, mainThreadTimeout, evalReply(t, "0\tAssets/A.unity\n"))
	if err != nil {
		t.Errorf("ListScenes: %v, want success after the re-query", err)
	}
	checkEvalCalls(t, calls, 2, slot, listScenesCode)

	calls, slot, err = evalIn(t, list, mainThreadTimeout, mainThreadTimeout)
	var timedOut *TimedOutError
	if !errors.As(err, &timedOut) || !errors.Is(err, ErrMayHaveRun) {
		t.Errorf("ListScenes: %v, want a TimedOutError marked ErrMayHaveRun", err)
	}
	checkEvalCalls(t, calls, 2, slot, listScenesCode)
}

// A reply ListScenes cannot read as a listing is a failure, and not a timeout: the captured eval's
// own result, a version, and a reply with no result string at all, save_all's.
func TestListScenesRefusesAReplyThatIsNoListing(t *testing.T) {
	for _, reply := range []fake.Reply{{Capture: "command-eval-first-call.json"},
		{Capture: "command-save-all.json"}} {
		t.Run(reply.Capture, func(t *testing.T) {
			calls, slot, err := evalIn(t, func(c Client, slot string) error {
				_, err := c.ListScenes(context.Background(), slot)
				return err
			}, reply)
			var f *output.Failure
			var timedOut *TimedOutError
			if !errors.As(err, &f) || f.Code() != output.CodeCommandFailed ||
				errors.As(err, &timedOut) {
				t.Errorf("ListScenes: %v, want a failure that is not a timeout", err)
			}
			checkEvalCalls(t, calls, 1, slot, listScenesCode)
		})
	}
}

// SaveScene saves the one scene at its path, written as a C# verbatim string whose quotes are
// doubled, and succeeds only when the code says it saved it.
func TestSaveSceneSavesThatSceneAlone(t *testing.T) {
	const path = `Assets/A "b".unity`
	save := func(c Client, slot string) error {
		return c.SaveScene(context.Background(), slot, path)
	}
	calls, _, err := evalIn(t, save, evalReply(t, "saved"))
	if err != nil {
		t.Fatalf("SaveScene: %v", err)
	}
	if len(calls) != 1 || len(calls[0].Args) != 5 ||
		!strings.Contains(calls[0].Args[2], `GetSceneByPath(@"Assets/A ""b"".unity")`) ||
		!strings.Contains(calls[0].Args[2], "EditorSceneManager.SaveScene(s)") {
		t.Errorf("unity calls = %+v, want one eval saving %s alone", calls, path)
	}

	for _, returned := range []string{"not saved", "not open"} {
		_, _, err := evalIn(t, save, evalReply(t, returned))
		var timedOut *TimedOutError
		if err == nil || !strings.Contains(err.Error(), "did not save "+path+": "+returned) ||
			errors.As(err, &timedOut) {
			t.Errorf("SaveScene returning %q: %v, want it not saved", returned, err)
		}
	}
}

// A save of one scene that meets the main-thread timeout is sent once more, as save_all's is
// (A29), and a second timeout is a TimedOutError.
func TestSaveSceneReQueriesOnceAfterAMainThreadTimeout(t *testing.T) {
	save := func(c Client, slot string) error {
		return c.SaveScene(context.Background(), slot, "Assets/A.unity")
	}
	calls, _, err := evalIn(t, save, mainThreadTimeout, evalReply(t, "saved"))
	if err != nil || len(calls) != 2 {
		t.Errorf("SaveScene: %v after %d calls, want success after the re-query", err, len(calls))
	}
	calls, _, err = evalIn(t, save, mainThreadTimeout, mainThreadTimeout)
	var timedOut *TimedOutError
	if !errors.As(err, &timedOut) || len(calls) != 2 {
		t.Errorf("SaveScene: %v after %d calls, want a TimedOutError after two", err, len(calls))
	}
}

// hangingRunner is a Runner whose every call goes unanswered until its context ends, as a save the
// Editor cannot take while a native panel holds it. It counts its calls.
type hangingRunner struct{ calls int }

func (r *hangingRunner) Run(ctx context.Context, _ string, _ []string, args ...string) (Result,
	error) {
	r.calls++
	<-ctx.Done()
	return Result{}, fmt.Errorf("running unity %s: %w", strings.Join(args, " "), ctx.Err())
}

// Each call that saves or lists scenes is bounded by SaveTimeout: one the Editor does not answer
// fails as a TimedOutError at exactly the bound, with no re-query, since a second call would wait
// on what held the first (#280). The wait passes on testing/synctest's clock.
func TestSavesThatGetNoAnswerTimeOutAtTheBound(t *testing.T) {
	for name, call := range map[string]func(c Client) error{
		"save_all": func(c Client) error { return c.SaveAll(context.Background(), "/slot") },
		"listing": func(c Client) error {
			_, err := c.ListScenes(context.Background(), "/slot")
			return err
		},
		"one scene": func(c Client) error {
			return c.SaveScene(context.Background(), "/slot", "Assets/A.unity")
		},
	} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				r := &hangingRunner{}
				begin := time.Now()
				err := call(Client{Runner: r})
				if waited := time.Since(begin); waited != SaveTimeout {
					t.Errorf("waited %s, want %s", waited, SaveTimeout)
				}
				var timedOut *TimedOutError
				var f *output.Failure
				if !errors.As(err, &timedOut) || !errors.As(err, &f) ||
					f.Exit() != output.ExitFailure ||
					!strings.Contains(err.Error(), "did not answer within 20s") {
					t.Errorf("error = %v, want a TimedOutError exiting 1 naming the bound", err)
				}
				if r.calls != 1 {
					t.Errorf("unity called %d times, want once", r.calls)
				}
			})
		})
	}
}
