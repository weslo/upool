package output

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestExitCodesMatchContract(t *testing.T) {
	for name, tc := range map[string]struct{ got, want int }{
		"ExitOK":       {ExitOK, 0},
		"ExitFailure":  {ExitFailure, 1},
		"ExitNoTarget": {ExitNoTarget, 6},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %d, want %d", name, tc.got, tc.want)
		}
	}
}

func TestMarshalWritesEmptyListsNotNull(t *testing.T) {
	got, err := json.Marshal(Envelope{Success: true, Command: "which"})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"success":true,"command":"which","data":null,"errors":[],"warnings":[]}`
	if string(got) != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestMarshalKeepsErrorsAndWarnings(t *testing.T) {
	got, err := json.Marshal(Envelope{
		Command:  "take",
		Errors:   []Error{{Code: "COMMAND_FAILED", Message: "m"}},
		Warnings: []string{"w"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"success":false,"command":"take","data":null,"errors":[{"code":"COMMAND_FAILED","message":"m"}],"warnings":["w"]}`
	if string(got) != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

// Every full envelope the spike captured from the Unity CLI must decode into Envelope with no
// unknown top-level fields, since upool's envelope mirrors it.
func TestDecodesEveryCapturedUnityEnvelope(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", "testdata", "unity", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no fixtures found under testdata/unity")
	}
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			raw, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			dec := json.NewDecoder(bytes.NewReader(raw))
			dec.DisallowUnknownFields()
			var e Envelope
			if err := dec.Decode(&e); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if e.Command == "" {
				t.Error("command is empty")
			}
			if !e.Success && len(e.Errors) == 0 {
				t.Error("failed envelope has no errors")
			}
		})
	}
}

func TestDecodesCapturedErrorCode(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "unity", "status-until-ready-no-instances.json"))
	if err != nil {
		t.Fatal(err)
	}
	var e Envelope
	if err := json.Unmarshal(raw, &e); err != nil {
		t.Fatal(err)
	}
	if e.Success || len(e.Errors) != 1 || e.Errors[0].Code != "STATUS_NO_INSTANCES" {
		t.Errorf("got success=%v errors=%+v, want one STATUS_NO_INSTANCES error", e.Success, e.Errors)
	}
}
