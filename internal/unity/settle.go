package unity

import (
	"context"
	"errors"
	"time"

	"github.com/weslo/upool/internal/output"
	"github.com/weslo/upool/internal/proc"
)

// The settle after ready (#282). On the first benchmark run, after a merge brought new assets into
// a slot, `upool open --gui` reported the Editor ready while they were still importing, and the
// run's record says the session's next Unity call "was refused as still settling for a few
// seconds"; #282 describes the refusal as a 503, which nothing records (docs/contracts.md (d), "An
// Editor still settling after ready"). Neither that refusal's reply nor what `unity status`
// reports meanwhile is captured yet, so `upool open` asks the Editor itself: once it is ready, a
// read-only eval that needs its main thread, which an import holds.

// SettleProbe is the C# WaitSettled evaluates: the read-only call that answered at once on a
// freshly ready GUI Editor (testdata/captures/unity/command-eval-first-call.json), and whose first
// call on another timed out on the Editor's main thread (spike §8 C.3;
// testdata/captures/unity/command-eval-main-thread-timeout.json).
const SettleProbe = "return Application.unityVersion;"

const (
	// SettlePollInterval is how long WaitSettled waits after a probe that failed before it sends
	// the next.
	SettlePollInterval = time.Second
	// settleProbeTimeout bounds one probe: twice the 5,000 ms the Pipeline server gives a
	// main-thread operation before it fails it (command-eval-main-thread-timeout.json), so that a
	// CLI that hangs cannot hold the wait.
	settleProbeTimeout = 10 * time.Second
)

// WaitSettled waits for the Editor of project, process pid, which `unity status --until-ready`
// has reported ready, to settle: for `unity command eval` of SettleProbe to succeed. It probes
// once at once, then every SettlePollInterval after a probe that failed, and returns true at the
// first that succeeds, or false when one fails at or after deadline, by now. Every failed probe
// counts as still settling, whatever its code, message or exit: the settling refusal's reply is not
// captured (docs/contracts.md (d)), so it cannot be told from the others, and the main-thread
// timeout is the Editor's main thread still busy. The Editor's process is checked after each
// failed probe, so an Editor that exits fails at once, as in WaitReady, rather than passing for
// one still settling.
func (c Client) WaitSettled(ctx context.Context, procs proc.Checker, project string, pid int,
	deadline time.Time, now func() time.Time) (bool, error) {
	exited := output.Fail(output.CodeCommandFailed,
		"the Editor of %s, process %d, exited after it was ready, before it settled", project, pid)
	start, err := procs.Start(pid)
	if errors.Is(err, proc.ErrNoProcess) {
		return false, exited
	}
	if err != nil {
		return false, err
	}
	for {
		if c.probeSettled(ctx, project) {
			return true, nil
		}
		alive, err := procs.Alive(pid, start)
		if err != nil {
			return false, err
		}
		if !alive {
			return false, exited
		}
		if !now().Before(deadline) {
			return false, nil
		}
		time.Sleep(SettlePollInterval)
	}
}

// probeSettled reports whether one `unity command eval` of SettleProbe, bounded by
// settleProbeTimeout, succeeded against the Editor of project.
func (c Client) probeSettled(ctx context.Context, project string) bool {
	ctx, cancel := context.WithTimeout(ctx, settleProbeTimeout)
	defer cancel()
	reply, err := c.Call(ctx, VerbCommand, project, "eval", SettleProbe)
	return err == nil && reply.Err() == nil
}
