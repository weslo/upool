package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/weslo/upool/internal/unity"
)

// checkVCS names the lines that report the repository's VCS set-up.
const checkVCS = "vcs"

// vcsDoctorTimeout bounds `unity vcs doctor`, so that a CLI that hangs cannot hang doctor. It took
// 0.14 s on a clone of the spike's project.
const vcsDoctorTimeout = 30 * time.Second

// vcsFixers is the last vcs line: the commands that would act on the findings, which upool runs
// none of.
const vcsFixers = "unity vcs doctor --fix and unity vcs merge-setup would act on these; upool " +
	"runs neither, since the repository's VCS set-up is its owner's (A35)"

// vcsChecks reports the repository's VCS set-up as `unity vcs doctor <repo>` sees it, check-only
// (docs/design.md, "Lanes: scripts in parallel, scenes one at a time, `.meta` never
// regenerated"; A35). Each finding warns, with its code, its severity and its message, and a last
// line names the commands that would act on them; a report with no findings passes. Without unity
// on PATH, or when the call fails, it warns and says why: the set-up is the owner's to change, so
// nothing here fails doctor.
func vcsChecks(ctx context.Context, inv *invocation, repo string) []doctorCheck {
	ctx, cancel := context.WithTimeout(ctx, vcsDoctorTimeout)
	defer cancel()
	findings, err := unity.Client{Runner: unity.Exec{}, Env: inv.environ()}.VCSDoctor(ctx, repo)
	switch {
	case errors.Is(err, unity.ErrNoUnity):
		return []doctorCheck{{checkVCS, checkWarn,
			"unity is not on PATH, so the repository's VCS set-up is not checked"}}
	case len(findings) == 0 && err != nil:
		return []doctorCheck{{checkVCS, checkWarn,
			"the repository's VCS set-up is not checked: " + err.Error()}}
	case len(findings) == 0:
		return []doctorCheck{{checkVCS, checkPass, "unity vcs doctor found nothing to fix"}}
	}
	checks := make([]doctorCheck, 0, len(findings)+1)
	for _, f := range findings {
		checks = append(checks, doctorCheck{checkVCS, checkWarn,
			fmt.Sprintf("%s (%s): %s", f.Code, f.Severity, f.Message)})
	}
	return append(checks, doctorCheck{checkVCS, checkWarn, vcsFixers})
}
