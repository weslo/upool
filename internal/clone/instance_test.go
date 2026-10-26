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

func (unsupportedProcs) Name(int) (string, error) { return "", errors.ErrUnsupported }

func (unsupportedProcs) EditorsAt(string) ([]int, error) { return nil, errors.ErrUnsupported }

// An instance file counts only when the process it names is running and is a Unity Editor: a dead
// Editor's file, one whose PID a process that is not Unity now has, one that names no process,
// and a ProtocolInstance.json alone are leftovers. Off macOS, where no process can be read, the
// file counts as it always did (A45; #243).
func TestEditorOpenJudgesTheEditorItsInstanceFileNames(t *testing.T) {
	const byFile = editorInstanceFile + " names a running Editor"
	for _, tt := range []struct {
		name    string
		content string
		procs   proc.Checker
		want    string
	}{
		{"a running Editor", `{"process_id": 5200}`, seedProcs{editorPID: procStart}, byFile},
		{"a dead Editor", `{"process_id": 5200}`, seedProcs{}, ""},
		{"a process that is not Unity", `{"process_id": 5100}`, seedProcs{clonerPID: procStart},
			""},
		{"no process_id", `{}`, seedProcs{editorPID: procStart}, ""},
		{"not JSON", `not json`, seedProcs{editorPID: procStart}, ""},
		{"no process to read", `{"process_id": 5200}`, unsupportedProcs{}, byFile},
		{"no file", "", seedProcs{editorPID: procStart}, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := testenv.New(t)
			if tt.content != "" {
				e.WriteFile(testenv.EditorInstanceFile, tt.content)
			}
			e.WriteFile("Library/ProtocolInstance.json", "[]\n")
			if got, err := EditorOpen(e.Repo, tt.procs); err != nil || got != tt.want {
				t.Errorf("EditorOpen = %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

// openerProcs is procs with EditorsAt finding pids at dir, or failing with err.
type openerProcs struct {
	proc.Checker
	dir  string
	pids []int
	err  error
}

func (o openerProcs) EditorsAt(dir string) ([]int, error) {
	if dir != o.dir {
		return nil, o.err
	}
	return o.pids, o.err
}

// An Editor that has the checkout as its project, which procs.EditorsAt finds by its arguments or
// its Temp/UnityLockfile, has it open before it writes EditorInstance.json: EditorOpen says which
// process. The instance file is asked first. An error from EditorsAt is EditorOpen's, and off macOS
// it counts for nothing (A49; #255).
func TestEditorOpenCountsAnEditorThatHasTheCheckoutAsItsProject(t *testing.T) {
	e := testenv.New(t)
	starting := openerProcs{Checker: seedProcs{}, dir: e.Repo, pids: []int{6100}}
	if got, err := EditorOpen(e.Repo, starting); err != nil ||
		got != "process 6100, a Unity Editor, has it as its project" {
		t.Errorf("EditorOpen = %q, %v; want process 6100 named", got, err)
	}
	if got, err := EditorOpen(t.TempDir(), starting); err != nil || got != "" {
		t.Errorf("EditorOpen of another checkout = %q, %v; want nothing", got, err)
	}
	e.WriteFile(testenv.EditorInstanceFile, `{"process_id": 5200}`)
	both := starting
	both.Checker = seedProcs{editorPID: procStart}
	if got, err := EditorOpen(e.Repo, both); err != nil ||
		got != editorInstanceFile+" names a running Editor" {
		t.Errorf("EditorOpen = %q, %v; want the instance file's reason first", got, err)
	}
	broken := openerProcs{Checker: seedProcs{}, dir: e.Repo, err: errors.New("sysctl failed")}
	if got, err := EditorOpen(e.Repo, broken); err == nil || got != "" {
		t.Errorf("EditorOpen = %q, %v; want EditorsAt's error", got, err)
	}
	off := openerProcs{Checker: seedProcs{}, dir: e.Repo, err: errors.ErrUnsupported}
	if got, err := EditorOpen(e.Repo, off); err != nil || got != "" {
		t.Errorf("EditorOpen off macOS = %q, %v; want nothing", got, err)
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
