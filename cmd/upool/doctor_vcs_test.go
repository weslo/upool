package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/weslo/upool/internal/output"
	"github.com/weslo/upool/internal/unity"
	"github.com/weslo/upool/internal/unity/fake"
)

// capturedFindings are the findings of testdata/captures/unity/vcs-doctor.json, the reply
// vcsDoctorReply replays.
func capturedFindings(t *testing.T) []unity.Finding {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "captures", "unity",
		"vcs-doctor.json"))
	if err != nil {
		t.Fatal(err)
	}
	var reply struct {
		Data struct {
			Findings []unity.Finding `json:"findings"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &reply); err != nil {
		t.Fatal(err)
	}
	return reply.Data.Findings
}

// vcsChecksOf are the vcs lines among checks.
func vcsChecksOf(checks []doctorCheck) []doctorCheck {
	return slices.DeleteFunc(slices.Clone(checks), func(c doctorCheck) bool {
		return c.Name != checkVCS
	})
}

// The acceptance test of #88: doctor runs `unity vcs doctor <repo>` check-only, with the
// repository's path and never --fix, and reports each finding of the check-only capture as a warn
// naming its code, then a line naming the commands that would act on them (A35). Warnings alone
// exit 0: the set-up is the repository owner's.
func TestDoctorReportsVCS(t *testing.T) {
	p := newHarnessPool(t)
	unityReply, claude := capturedTools(t)
	u, _ := p.installTools(t, unityReply, claude)
	r := runIn(t, p.Env, p.Repo, "doctor", "--json")
	if r.code != output.ExitOK {
		t.Errorf("exit %d, want 0; stdout %s", r.code, r.stdout)
	}

	var vcsCalls int
	for _, c := range u.Calls() {
		if slices.Contains(c.Args, "--fix") {
			t.Errorf("unity %v passes --fix", c.Args)
		}
		if len(c.Args) > 0 && c.Args[0] == "vcs" {
			vcsCalls++
			want := []string{"vcs", "doctor", p.Repo, "--format", "json"}
			if !slices.Equal(c.Args, want) || c.ProjectPath != p.Repo || c.Dir != p.Repo {
				t.Errorf("unity %v with UNITY_PROJECT_PATH %q in %s, want unity %v in the "+
					"repository", c.Args, c.ProjectPath, c.Dir, want)
			}
		}
	}
	if vcsCalls != 1 {
		t.Errorf("unity calls = %+v, want one vcs doctor", u.Calls())
	}

	got := vcsChecksOf(doctorChecks(t, r))
	findings := capturedFindings(t)
	if len(got) != len(findings)+1 {
		t.Fatalf("vcs checks = %+v, want a line per finding of %d, then the fixers", got,
			len(findings))
	}
	for i, f := range findings {
		want := f.Code + " (" + f.Severity + "): " + f.Message
		if got[i].Result != checkWarn || got[i].Reason != want {
			t.Errorf("vcs check %d = %s, %q; want warn, %q", i, got[i].Result, got[i].Reason,
				want)
		}
	}
	if last := got[len(findings)]; last.Result != checkWarn ||
		!strings.Contains(last.Reason, "unity vcs doctor --fix") ||
		!strings.Contains(last.Reason, "unity vcs merge-setup") {
		t.Errorf("last vcs check = %s, %q; want warn, naming unity vcs doctor --fix and "+
			"unity vcs merge-setup", last.Result, last.Reason)
	}
}

// Without unity on PATH, and when the call fails, the vcs check warns and says why; neither fails
// doctor. A reply that is not JSON stands for the failed call: `unity --version`'s text.
func TestDoctorVCSNeverFails(t *testing.T) {
	t.Run("no unity", func(t *testing.T) {
		p := newHarnessPool(t) // with only git on PATH
		r := runIn(t, p.Env, p.Repo, "doctor", "--json")
		got := vcsChecksOf(doctorChecks(t, r))
		want := []doctorCheck{{checkVCS, checkWarn,
			"unity is not on PATH, so the repository's VCS set-up is not checked"}}
		if r.code != output.ExitOK || !slices.Equal(got, want) {
			t.Errorf("exit %d, vcs checks %+v; want 0 and %+v", r.code, got, want)
		}
	})
	t.Run("a failed call", func(t *testing.T) {
		p := newHarnessPool(t)
		version := capturedVersion(t, "unity", "version.raw.txt")
		u := fake.Install(t, version, licenceReply, version)
		p.Setenv("PATH", u.Dir()+string(os.PathListSeparator)+p.Getenv("PATH"))
		r := runIn(t, p.Env, p.Repo, "doctor", "--json")
		got := vcsChecksOf(doctorChecks(t, r))
		if r.code != output.ExitOK || len(got) != 1 || got[0].Result != checkWarn ||
			!strings.HasPrefix(got[0].Reason, "the repository's VCS set-up is not checked: "+
				"unity vcs doctor "+p.Repo+" exited 0 with a reply that is not JSON") {
			t.Errorf("exit %d, vcs checks %+v; want 0 and one warning saying why", r.code, got)
		}
	})
}
