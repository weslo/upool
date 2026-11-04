package unity

import (
	"context"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// hangOnceRunner is a Runner whose first call hangs until its context ends, as a CLI that never
// answers, and whose later calls reply with reply. It records each call's arguments.
type hangOnceRunner struct {
	reply []byte

	mu    sync.Mutex
	calls [][]string
}

func (r *hangOnceRunner) Run(ctx context.Context, _ string, _ []string, args ...string) (Result,
	error) {
	r.mu.Lock()
	r.calls = append(r.calls, args)
	n := len(r.calls)
	r.mu.Unlock()
	if n == 1 {
		<-ctx.Done()
		return Result{}, ctx.Err()
	}
	return Result{Stdout: r.reply}, nil
}

// runningProcs is a proc.Checker in which every process is running throughout.
type runningProcs struct{}

func (runningProcs) Start(int) (time.Time, error)       { return time.Unix(1, 0), nil }
func (runningProcs) Alive(int, time.Time) (bool, error) { return true, nil }
func (runningProcs) Name(int) (string, error)           { return "Unity", nil }
func (runningProcs) EditorsAt(string) ([]int, error)    { return nil, nil }

// A settle probe the CLI never answers is cut at settleProbeTimeout and counts as still settling,
// so that a CLI that hangs cannot hold the wait (#282). Before the deadline, the next probe goes a
// poll interval later, and its answer, testdata/captures/unity/command-eval-first-call.json, is the
// Editor settled; a cut probe at the deadline is the last, so the wait runs past the deadline by at
// most one probe. The waits pass on testing/synctest's clock.
func TestWaitSettledCutsAProbeThatHangs(t *testing.T) {
	probe := []string{"command", "eval", SettleProbe, "--format", "json"}
	for _, tt := range []struct {
		name     string
		deadline time.Duration
		settled  bool
		waited   time.Duration
		calls    int
	}{
		{"before the deadline", ReadyTimeout, true, settleProbeTimeout + SettlePollInterval, 2},
		{"at the deadline", settleProbeTimeout / 2, false, settleProbeTimeout, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				r := &hangOnceRunner{reply: captured(t, "command-eval-first-call.json")}
				begin := time.Now()
				settled, err := Client{Runner: r}.WaitSettled(t.Context(), runningProcs{},
					"/work/slot", 25147, begin.Add(tt.deadline), time.Now)
				waited := time.Since(begin)
				if err != nil || settled != tt.settled || waited != tt.waited {
					t.Errorf("WaitSettled = %t, %v after %s; want %t, no error, after %s",
						settled, err, waited, tt.settled, tt.waited)
				}
				if len(r.calls) != tt.calls || slices.ContainsFunc(r.calls,
					func(c []string) bool { return !slices.Equal(c, probe) }) {
					t.Errorf("unity calls = %q, want %d of %q", r.calls, tt.calls, probe)
				}
			})
		})
	}
}
