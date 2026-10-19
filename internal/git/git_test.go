package git

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// The recorded outputs below are git 2.55.0's bytes for the states their tests name, with each
// temporary directory shortened to /work.

const recordedHEAD = "2aab586c933645c9cceba0791b69bf201ef5a64c"

// recordedWorktreeList is `git worktree list --porcelain -z` in a repository with a detached
// worktree, one locked with a reason, one locked without, and one whose directory was deleted.
const recordedWorktreeList = "worktree /work/repo\x00HEAD " + recordedHEAD + "\x00branch refs/heads/main\x00\x00" +
	"worktree /work/wt-detached\x00HEAD " + recordedHEAD + "\x00detached\x00\x00" +
	"worktree /work/wt-locked\x00HEAD " + recordedHEAD + "\x00branch refs/heads/feature\x00locked slot in use\x00\x00" +
	"worktree /work/wt-locked-noreason\x00HEAD " + recordedHEAD + "\x00branch refs/heads/plain\x00locked\x00\x00" +
	"worktree /work/wt-prunable\x00HEAD " + recordedHEAD + "\x00detached\x00prunable gitdir file points to non-existent location\x00\x00"

// recordedBareWorktreeList is the same command in a bare repository.
const recordedBareWorktreeList = "worktree /work/bare.git\x00bare\x00\x00"

// recordedStatus is `git status --porcelain=v1 -z` after a modification, a deletion, a staged new
// file, a staged rename and an untracked file whose name has a space.
const recordedStatus = " M b.txt\x00 D c.txt\x00A  new.txt\x00R  renamed.txt\x00a.txt\x00?? un tracked.txt\x00"

// call is one Run a fakeRunner received.
type call struct {
	dir  string
	env  []string
	args []string
}

// fakeRunner replies to every Run with res and err, and records the call.
type fakeRunner struct {
	res   Result
	err   error
	calls []call
}

func (f *fakeRunner) Run(
	_ context.Context, dir string, env []string, args ...string,
) (Result, error) {
	f.calls = append(f.calls, call{dir, env, args})
	return f.res, f.err
}

// replying returns a Client whose runner replies with stdout and exit code 0.
func replying(stdout string) (Client, *fakeRunner) {
	r := &fakeRunner{res: Result{Stdout: []byte(stdout)}}
	return Client{Runner: r, Env: []string{"HOME=/home"}}, r
}

// Each read sends exactly one git call, in the directory and environment it was given.
func TestReadsSendOneCallWithTheirArguments(t *testing.T) {
	tests := []struct {
		name string
		read func(Client) error
		want []string
	}{
		{"Status", func(c Client) error {
			_, err := c.Status(context.Background(), "/work/slot")
			return err
		}, []string{"status", "--porcelain=v1", "-z", "--untracked-files=normal"}},
		{"WorktreeList", func(c Client) error {
			_, err := c.WorktreeList(context.Background(), "/work/slot")
			return err
		}, []string{"worktree", "list", "--porcelain", "-z"}},
		{"HeadReachable", func(c Client) error {
			_, err := c.HeadReachable(context.Background(), "/work/slot")
			return err
		}, []string{"for-each-ref", "--contains", "HEAD", "--format=%(refname)"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, r := replying("")
			if err := tt.read(c); err != nil {
				t.Fatal(err)
			}
			if len(r.calls) != 1 {
				t.Fatalf("calls = %d, want 1", len(r.calls))
			}
			got := r.calls[0]
			if got.dir != "/work/slot" || !slices.Equal(got.env, c.Env) || !slices.Equal(got.args, tt.want) {
				t.Errorf("call = %+v, want dir /work/slot, env %v, args %v", got, c.Env, tt.want)
			}
		})
	}
}

func TestStatusParsesRecordedEntries(t *testing.T) {
	c, _ := replying(recordedStatus)
	got, err := c.Status(context.Background(), "/work/repo")
	if err != nil {
		t.Fatal(err)
	}
	want := []StatusEntry{
		{XY: " M", Path: "b.txt"},
		{XY: " D", Path: "c.txt"},
		{XY: "A ", Path: "new.txt"},
		{XY: "R ", Path: "renamed.txt", OrigPath: "a.txt"},
		{XY: "??", Path: "un tracked.txt"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Status = %+v, want %+v", got, want)
	}
}

// A copy is followed by its original path, as a rename is.
func TestStatusReadsOriginalPathOfCopy(t *testing.T) {
	c, _ := replying("C  copy.txt\x00orig.txt\x00 M other.txt\x00")
	got, err := c.Status(context.Background(), "/work/repo")
	if err != nil {
		t.Fatal(err)
	}
	want := []StatusEntry{
		{XY: "C ", Path: "copy.txt", OrigPath: "orig.txt"},
		{XY: " M", Path: "other.txt"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Status = %+v, want %+v", got, want)
	}
}

func TestStatusOfCleanWorktreeIsEmpty(t *testing.T) {
	c, _ := replying("")
	got, err := c.Status(context.Background(), "/work/repo")
	if err != nil || len(got) != 0 {
		t.Errorf("Status = %+v, %v, want no entries and no error", got, err)
	}
}

func TestStatusRejectsMalformedOutput(t *testing.T) {
	for name, out := range map[string]string{
		"no final NUL":       "?? a.txt",
		"short entry":        "??\x00",
		"no space after XY":  "??a.txt\x00",
		"rename without old": "R  new.txt\x00",
	} {
		t.Run(name, func(t *testing.T) {
			c, _ := replying(out)
			if got, err := c.Status(context.Background(), "/work/repo"); err == nil {
				t.Errorf("Status = %+v, nil, want an error", got)
			}
		})
	}
}

func TestWorktreeListParsesRecordedRecords(t *testing.T) {
	c, _ := replying(recordedWorktreeList)
	got, err := c.WorktreeList(context.Background(), "/work/repo")
	if err != nil {
		t.Fatal(err)
	}
	want := []Worktree{
		{Path: "/work/repo", HEAD: recordedHEAD, Branch: "refs/heads/main"},
		{Path: "/work/wt-detached", HEAD: recordedHEAD, Detached: true},
		{Path: "/work/wt-locked", HEAD: recordedHEAD, Branch: "refs/heads/feature", Locked: true,
			LockReason: "slot in use"},
		{Path: "/work/wt-locked-noreason", HEAD: recordedHEAD, Branch: "refs/heads/plain", Locked: true},
		{Path: "/work/wt-prunable", HEAD: recordedHEAD, Detached: true, Prunable: true,
			PrunableReason: "gitdir file points to non-existent location"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("WorktreeList =\n%+v\nwant\n%+v", got, want)
	}
}

func TestWorktreeListParsesBareRepository(t *testing.T) {
	c, _ := replying(recordedBareWorktreeList)
	got, err := c.WorktreeList(context.Background(), "/work/bare.git")
	if err != nil {
		t.Fatal(err)
	}
	want := []Worktree{{Path: "/work/bare.git", Bare: true}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("WorktreeList = %+v, want %+v", got, want)
	}
}

// git says the porcelain format may gain attributes; one upool does not know is skipped.
func TestWorktreeListSkipsUnknownAttributes(t *testing.T) {
	c, _ := replying("worktree /work/repo\x00HEAD " + recordedHEAD + "\x00future value\x00detached\x00\x00")
	got, err := c.WorktreeList(context.Background(), "/work/repo")
	if err != nil {
		t.Fatal(err)
	}
	want := []Worktree{{Path: "/work/repo", HEAD: recordedHEAD, Detached: true}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("WorktreeList = %+v, want %+v", got, want)
	}
}

func TestWorktreeListRejectsMalformedOutput(t *testing.T) {
	for name, out := range map[string]string{
		"no final NUL":         "worktree /work/repo\x00HEAD " + recordedHEAD,
		"unterminated record":  "worktree /work/repo\x00detached\x00",
		"two records unclosed": "worktree /work/a\x00worktree /work/b\x00\x00",
		"attribute first":      "detached\x00\x00",
		"empty record":         "\x00",
	} {
		t.Run(name, func(t *testing.T) {
			c, _ := replying(out)
			if got, err := c.WorktreeList(context.Background(), "/work/repo"); err == nil {
				t.Errorf("WorktreeList = %+v, nil, want an error", got)
			}
		})
	}
}

func TestHeadReachableFollowsWhetherARefIsPrinted(t *testing.T) {
	for out, want := range map[string]bool{
		"":                                       false,
		"refs/heads/main\n":                      true,
		"refs/heads/main\nrefs/remotes/o/main\n": true,
	} {
		c, _ := replying(out)
		got, err := c.HeadReachable(context.Background(), "/work/slot")
		if err != nil || got != want {
			t.Errorf("HeadReachable with output %q = %v, %v, want %v, nil", out, got, err, want)
		}
	}
}

// A git call that exits non-zero is an error naming the call and carrying git's stderr, from every
// read.
func TestNonZeroExitIsAnErrorWithStderr(t *testing.T) {
	stderr := "fatal: not a git repository (or any of the parent directories): .git\n"
	reads := map[string]func(Client) error{
		"Status": func(c Client) error {
			_, err := c.Status(context.Background(), "/work/none")
			return err
		},
		"WorktreeList": func(c Client) error {
			_, err := c.WorktreeList(context.Background(), "/work/none")
			return err
		},
		"HeadReachable": func(c Client) error {
			_, err := c.HeadReachable(context.Background(), "/work/none")
			return err
		},
	}
	for name, read := range reads {
		t.Run(name, func(t *testing.T) {
			r := &fakeRunner{res: Result{Stderr: []byte(stderr), ExitCode: 128}}
			err := read(Client{Runner: r})
			if err == nil {
				t.Fatal("err = nil, want an error")
			}
			msg := err.Error()
			wantEnd := ": exit status 128: " + strings.TrimSpace(stderr)
			if !strings.HasPrefix(msg, "git -C /work/none ") || !strings.HasSuffix(msg, wantEnd) {
				t.Errorf("err = %q, want the call, exit status 128 and git's stderr", msg)
			}
		})
	}
}

func TestRunnerErrorIsReturned(t *testing.T) {
	want := errors.New("git not found")
	c := Client{Runner: &fakeRunner{err: want}}
	if _, err := c.Status(context.Background(), "/work/repo"); !errors.Is(err, want) {
		t.Errorf("err = %v, want %v", err, want)
	}
}
