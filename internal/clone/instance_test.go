package clone

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/weslo/upool/internal/proc"
	"github.com/weslo/upool/internal/testenv"
)

// unsupportedProcs reads no start time, as internal/proc off macOS.
type unsupportedProcs struct{}

func (unsupportedProcs) Start(int) (time.Time, error) {
	return time.Time{}, errors.ErrUnsupported
}

func (unsupportedProcs) Alive(int, time.Time) (bool, error) { return false, errors.ErrUnsupported }

// An instance file counts when the Editor it names is running: its process exists and started no
// later than the file was written. A dead Editor's file, one whose PID a later process reused, and
// a ProtocolInstance.json alone are leftovers; a file naming no process, or one whose process
// cannot be read, counts, since the Editor may be running (A45; #243).
func TestInstanceFileJudgesTheEditorItNames(t *testing.T) {
	written := procStart.Add(10 * time.Second)
	for _, tt := range []struct {
		name    string
		content string
		procs   proc.Checker
		want    string
	}{
		{"a running Editor", `{"process_id": 5200}`, seedProcs{editorPID: procStart},
			editorInstanceFile},
		{"a dead Editor", `{"process_id": 5200}`, seedProcs{}, ""},
		{"a later process with its PID", `{"process_id": 5200}`,
			seedProcs{editorPID: written.Add(time.Hour)}, ""},
		{"no process_id", `{}`, seedProcs{}, editorInstanceFile},
		{"not JSON", `not json`, seedProcs{}, editorInstanceFile},
		{"no start time to read", `{"process_id": 5200}`, unsupportedProcs{}, editorInstanceFile},
		{"no file", "", seedProcs{}, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := testenv.New(t)
			if tt.content != "" {
				e.WriteFile(testenv.EditorInstanceFile, tt.content)
				if err := os.Chtimes(e.Path(testenv.EditorInstanceFile), written,
					written); err != nil {
					t.Fatal(err)
				}
			}
			e.WriteFile("Library/ProtocolInstance.json", "[]\n")
			if got, err := InstanceFile(e.Repo, tt.procs); err != nil || got != tt.want {
				t.Errorf("InstanceFile = %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

// ClearInstanceFiles removes EditorInstance.json and the files beside it only when it names one of
// the Editor's processes, and reports what it removed.
func TestClearInstanceFilesRemovesOnlyTheNamedEditorsFiles(t *testing.T) {
	files := []string{testenv.EditorInstanceFile, "Library/ProtocolInstance.json",
		"Temp/UnityLockfile"}
	for _, tt := range []struct {
		name string
		pids []int
		want []string
	}{
		{"its Editor's", []int{1, editorPID}, files},
		{"another Editor's", []int{1, 2}, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := testenv.New(t)
			e.WriteFile(testenv.EditorInstanceFile, `{"process_id": 5200}`)
			e.WriteFile("Library/ProtocolInstance.json", "[]\n")
			e.WriteFile("Temp/UnityLockfile", "")
			got, err := ClearInstanceFiles(e.Repo, tt.pids)
			if err != nil || !slices.Equal(got, tt.want) {
				t.Errorf("ClearInstanceFiles = %q, %v; want %q", got, err, tt.want)
			}
			for _, name := range files {
				_, err := os.Stat(filepath.Join(e.Repo, filepath.FromSlash(name)))
				if gone := err != nil; gone != slices.Contains(tt.want, name) {
					t.Errorf("%s: %v; want it gone: %t", name, err, slices.Contains(tt.want, name))
				}
			}
		})
	}
}
