package ledger

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/weslo/upool/internal/proc"
)

// liveProcs is a proc.Checker whose Start knows the PIDs it lists and fails with err for any
// other, ErrNoProcess when err is nil.
type liveProcs struct {
	pids map[int]bool
	err  error
}

func (l liveProcs) Start(pid int) (time.Time, error) {
	if l.pids[pid] {
		return time.Unix(0, 0), nil
	}
	if l.err != nil {
		return time.Time{}, l.err
	}
	return time.Time{}, proc.ErrNoProcess
}

func (l liveProcs) Alive(int, time.Time) (bool, error) { return false, nil }

// Name names every PID it lists a Unity Editor, as Start knows them.
func (l liveProcs) Name(pid int) (string, error) {
	if _, err := l.Start(pid); err != nil {
		return "", err
	}
	return "Unity", nil
}

// An Editor counts while its PID is a process and its project is the one asked about, spelled
// any way that comes to the same canonical path.
func TestLiveInFindsALiveEditorInTheProject(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	slot, other := filepath.Join(dir, "slot-1"), filepath.Join(dir, "slot-2")
	l := Ledger{Editors: []Editor{{Project: other, PID: 10}, {Project: slot + "/", PID: 20},
		{Project: slot, PID: 30}}}
	tests := []struct {
		name string
		pids map[int]bool
		want bool
	}{
		{"its Editor alive", map[int]bool{30: true}, true},
		{"its other entry alive, spelled with a trailing slash", map[int]bool{20: true}, true},
		{"only another project's Editor alive", map[int]bool{10: true}, false},
		{"none alive", nil, false},
	}
	for _, tt := range tests {
		got, err := l.LiveIn(liveProcs{pids: tt.pids}, slot)
		if err != nil || got != tt.want {
			t.Errorf("%s: LiveIn = %t, %v; want %t", tt.name, got, err, tt.want)
		}
	}
	broken := errors.New("sysctl failed")
	if _, err := l.LiveIn(liveProcs{err: broken}, slot); !errors.Is(err, broken) {
		t.Errorf("LiveIn with a failing check: error %v, want it to wrap %v", err, broken)
	}
}
