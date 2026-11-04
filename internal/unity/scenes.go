package unity

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/weslo/upool/internal/output"
)

// The wrappers `upool close` saves an Editor's scenes with, other than SaveAll: the listing of the
// open scenes before the save, and the save of one scene, for an Editor with a scene that has no
// path (#280). save_all takes no parameters, so it cannot be told to pass a scene over
// (testdata/captures/unity/command-list-query-save.json), and on the first benchmark run its save
// of a scene with no path, which the Editor's restore after a development build had left open,
// opened a native Save panel and held the Editor until a person dismissed it
// (docs/benchmarks/2026-11-03-three-sessions-shared-prefab.md). Both run `unity command eval`,
// the one way the CLI's captured replies show to read an Editor's state
// (testdata/captures/unity/command-eval-first-call.json).

// SaveTimeout bounds each call that saves an Editor's scenes or lists them first: SaveAll's,
// ListScenes' and SaveScene's. The spike's save_all took 0.73 s (spike §8 C.5) and the first
// benchmark run's 0.14 to 1.45 s, while the one a Save panel held was given up by the CLI at 30 s
// with a reply no capture records (#280). Bounded below that, a save the Editor cannot answer is
// upool's TimedOutError, whatever the CLI would have replied.
const SaveTimeout = 20 * time.Second

// TimedOutError is the failure of a call that saves an Editor's scenes, or lists them first, and
// timed out: SaveTimeout ran out, or the Editor's main thread timed out on the call and on its
// re-query (A29). Either way the Editor did not take the call, and a native panel holding its main
// thread, such as the Save panel a scene with no path opens, is the likely cause (#280).
type TimedOutError struct {
	// Err is the failure: an output failure, with the CLI's code for a main-thread timeout.
	Err error
}

func (e *TimedOutError) Error() string { return e.Err.Error() }

func (e *TimedOutError) Unwrap() error { return e.Err }

// bounded runs call with ctx bounded by SaveTimeout. A call the bound cut is a TimedOutError
// saying so, with what as the call.
func bounded(ctx context.Context, what string, call func(ctx context.Context) error) error {
	bctx, cancel := context.WithTimeout(ctx, SaveTimeout)
	defer cancel()
	err := call(bctx)
	if err != nil && ctx.Err() == nil && errors.Is(bctx.Err(), context.DeadlineExceeded) {
		return &TimedOutError{Err: output.Fail(output.CodeCommandFailed,
			"%s did not answer within %s", what, SaveTimeout)}
	}
	return err
}

// requeried runs call, and once more when it fails with the main-thread timeout, whose work may
// have been done anyway (ErrMayHaveRun): saving a saved scene changes nothing, and a listing
// changes nothing at all, so the second call is the re-query (A29). A second main-thread timeout
// is a TimedOutError, which still wraps ErrMayHaveRun. Any other failure is returned as it is.
func requeried(call func() error) error {
	err := call()
	if !errors.Is(err, ErrMayHaveRun) {
		return err
	}
	err = call()
	if errors.Is(err, ErrMayHaveRun) {
		return &TimedOutError{Err: err}
	}
	return err
}

// OpenScene is one scene open in an Editor, as ListScenes reports it.
type OpenScene struct {
	// Path is the scene's path in the project, such as Assets/Scenes/Main.unity, and "" for a
	// scene that has none, an Untitled one: saving it asks where with a native Save panel.
	Path string
	// Dirty is whether it has unsaved changes.
	Dirty bool
}

// listScenesCode is the C# ListScenes evaluates: one line per open scene, in the Editor's order,
// "1" for a scene with unsaved changes or "0", a tab, and the scene's path, which is empty for an
// Untitled scene. Every name is fully qualified, so it needs no using directive.
const listScenesCode = "System.Text.StringBuilder b = new System.Text.StringBuilder(); " +
	"for (int i = 0; i < UnityEngine.SceneManagement.SceneManager.sceneCount; i++) { " +
	"UnityEngine.SceneManagement.Scene s = " +
	"UnityEngine.SceneManagement.SceneManager.GetSceneAt(i); " +
	`b.Append(s.isDirty ? "1" : "0").Append("\t").Append(s.path).Append("\n"); } ` +
	"return b.ToString();"

// ListScenes returns the scenes open in the Editor of the slot at slot, each with its path and
// whether it has unsaved changes: `unity command eval` of listScenesCode, through the verb table,
// bounded by SaveTimeout and re-queried after a main-thread timeout (requeried). A timeout is a
// TimedOutError.
func (c Client) ListScenes(ctx context.Context, slot string) ([]OpenScene, error) {
	var listed string
	err := requeried(func() error {
		var err error
		listed, err = c.eval(ctx, slot, "the scene listing", listScenesCode)
		return err
	})
	if err != nil {
		return nil, err
	}
	var scenes []OpenScene
	for line := range strings.Lines(listed) {
		line = strings.TrimSuffix(line, "\n")
		if len(line) < 2 || line[0] != '0' && line[0] != '1' || line[1] != '\t' {
			return nil, output.Fail(output.CodeCommandFailed,
				"unity command eval %s listed the open scenes as %q, which is not one scene a "+
					"line", slot, listed)
		}
		scenes = append(scenes, OpenScene{Path: line[2:], Dirty: line[0] == '1'})
	}
	return scenes, nil
}

// SaveScene saves the open scene at path, one ListScenes reported, in the Editor of the slot at
// slot: `unity command eval` of EditorSceneManager.SaveScene on that scene alone, which saves it to
// its own path and asks nothing, bounded and re-queried as ListScenes is. It is how `upool close`
// saves the scenes that have a path when one does not, since save_all would save that one too.
func (c Client) SaveScene(ctx context.Context, slot, path string) error {
	code := "UnityEngine.SceneManagement.Scene s = " +
		"UnityEngine.SceneManagement.SceneManager.GetSceneByPath(" + verbatim(path) + "); " +
		`if (!s.IsValid()) { return "not open"; } ` +
		"return UnityEditor.SceneManagement.EditorSceneManager.SaveScene(s) ? " +
		`"saved" : "not saved";`
	var saved string
	err := requeried(func() error {
		var err error
		saved, err = c.eval(ctx, slot, "the save of "+path, code)
		return err
	})
	if err != nil {
		return err
	}
	if saved != "saved" {
		return output.Fail(output.CodeCommandFailed, "unity command eval %s did not save %s: %s",
			slot, path, saved)
	}
	return nil
}

// verbatim is s as a C# verbatim string literal, in which only a quote needs escaping, by
// doubling it.
func verbatim(s string) string {
	return `@"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// eval runs `unity command eval <code>` against the slot at slot, bounded by SaveTimeout, and
// returns the string the code returned: data.result.result in the reply
// (testdata/captures/unity/command-eval-first-call.json). what names the call in a timeout's
// message. A reply that failed is Reply.Err's failure; one whose code did not succeed, or returned
// no string, is a failure carrying the reply's result.
func (c Client) eval(ctx context.Context, slot, what, code string) (string, error) {
	var reply Reply
	err := bounded(ctx, "unity command eval "+slot+", "+what+",", func(ctx context.Context) error {
		var err error
		reply, err = c.Call(ctx, VerbCommand, slot, "eval", code)
		if err == nil {
			err = reply.Err()
		}
		return err
	})
	if err != nil {
		return "", err
	}
	var data struct {
		Result struct {
			Success bool            `json:"success"`
			Result  json.RawMessage `json:"result"`
		} `json:"result"`
	}
	var returned string
	if json.Unmarshal(reply.Response.Data, &data) != nil || !data.Result.Success ||
		json.Unmarshal(data.Result.Result, &returned) != nil {
		return "", output.Fail(output.CodeCommandFailed,
			"unity command eval %s, %s, returned no string: %s", slot, what,
			reply.Response.Data)
	}
	return returned, nil
}
