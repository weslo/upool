package unity

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

var capturesDir = filepath.Join("..", "..", "testdata", "captures", "unity")

func decodeCapture(t *testing.T, name string) Response {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(capturesDir, name))
	if err != nil {
		t.Fatal(err)
	}
	var r Response
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatalf("decode %s: %v", name, err)
	}
	return r
}

// Every full envelope the spike captured from the Unity CLI must decode into Response.
func TestDecodesEveryCapture(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(capturesDir, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatalf("no captures found under %s", capturesDir)
	}
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			r := decodeCapture(t, filepath.Base(f))
			if r.Command == "" {
				t.Error("command is empty")
			}
			if !r.Success && len(r.Errors) == 0 {
				t.Error("failed reply has no errors")
			}
		})
	}
}

func TestDecodesCapturedErrorCode(t *testing.T) {
	r := decodeCapture(t, "status-until-ready-no-instances.json")
	if r.Success || len(r.Errors) != 1 || r.Errors[0].Code != "STATUS_NO_INSTANCES" {
		t.Errorf("got success=%v errors=%+v, want one STATUS_NO_INSTANCES error", r.Success, r.Errors)
	}
}

// The recompile capture has no top-level errors or warnings.
func TestDecodesReplyWithoutErrorsOrWarnings(t *testing.T) {
	r := decodeCapture(t, "recompile-up-to-date.json")
	if !r.Success || r.Errors != nil || r.Warnings != nil {
		t.Errorf("got success=%v errors=%v warnings=%v, want success with neither", r.Success, r.Errors, r.Warnings)
	}
}

// A later CLI may add top-level fields; they must not break decoding.
func TestIgnoresUnknownTopLevelFields(t *testing.T) {
	var r Response
	raw := `{"success":true,"command":"status","data":{"count":0},"errors":[],"warnings":[],"durationMs":12}`
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		t.Fatal(err)
	}
	if !r.Success || string(r.Data) != `{"count":0}` {
		t.Errorf("got success=%v data=%s", r.Success, r.Data)
	}
}
