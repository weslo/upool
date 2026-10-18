package output

import (
	"encoding/json"
	"testing"
)

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
