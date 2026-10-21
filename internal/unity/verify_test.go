package unity

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"

	"github.com/weslo/upool/internal/output"
	"github.com/weslo/upool/internal/pathvars"
	"github.com/weslo/upool/internal/unity/fake"
)

// verifyIn calls Verify on a slot through the fake unity, which answers with reply, and returns
// the findings and the error, failing the test unless the fake recorded one call targeting the
// slot.
func verifyIn(t *testing.T, reply fake.Reply) ([]Finding, error) {
	t.Helper()
	slot := slotDir(t)
	repo := filepath.Dir(filepath.Dir(filepath.Dir(slot)))
	reply.Vars = []pathvars.Var{{Name: "repo", Path: repo}, {Name: "cwd", Path: slot}}
	u := fake.Install(t, reply)
	env := []string{"PATH=" + u.Dir(), "HOME=" + u.Dir()}
	findings, err := Client{Runner: Exec{}, Env: env}.Verify(context.Background(), slot)
	calls := u.Calls()
	if len(calls) != 1 {
		t.Fatalf("unity calls = %+v, want one", calls)
	}
	want := []string{"projects", "verify", slot, "--strict", "--format", "json"}
	if c := calls[0]; !slices.Equal(c.Args, want) || c.ProjectPath != slot || c.Dir != slot {
		t.Errorf("call unity %v with UNITY_PROJECT_PATH %q in %s, want unity %v targeting the slot",
			c.Args, c.ProjectPath, c.Dir, want)
	}
	return findings, err
}

// A clean slot passes, with no findings (testdata/captures/unity/projects-verify-pass.json).
func TestVerifyPasses(t *testing.T) {
	findings, err := verifyIn(t, fake.Reply{Capture: "projects-verify-pass.json"})
	if err != nil || len(findings) != 0 {
		t.Errorf("Verify = %+v, %v; want no findings and no error", findings, err)
	}
}

// A slot missing a .meta file fails with PROJECTS_VERIFY_FAILED, exiting 6 as the capture did
// (testdata/captures/unity/projects-verify-fail.json), and the finding that failed it.
func TestVerifyReturnsTheFindings(t *testing.T) {
	findings, err := verifyIn(t, fake.Reply{Capture: "projects-verify-fail.json",
		Exit: cliExitNoTarget})
	want := []Finding{{Code: "META_MISSING", Severity: "error",
		Path: "Assets/AddressableAssetsData", Message: "This asset has no .meta file next to " +
			"it, so Unity assigns a new guid and every existing reference to it breaks.",
		Hint: "Commit the .meta file next to the asset, or open the project in the Editor to " +
			"regenerate it and commit the result."}}
	if !slices.Equal(findings, want) {
		t.Errorf("findings = %+v, want %+v", findings, want)
	}
	var f *output.Failure
	if !errors.As(err, &f) || f.Code() != "PROJECTS_VERIFY_FAILED" {
		t.Errorf("error %v, want PROJECTS_VERIFY_FAILED", err)
	}
}

// A call that failed for another reason has no findings, only Reply.Err's error.
func TestVerifyFailureWithNoFindings(t *testing.T) {
	findings, err := verifyIn(t, fake.Reply{Capture: "build-not-a-unity-project.json",
		Exit: cliExitNoTarget})
	var f *output.Failure
	if len(findings) != 0 || !errors.As(err, &f) || f.Exit() != output.ExitNoTarget {
		t.Errorf("Verify = %+v, %v; want no findings and the not-a-project failure", findings,
			err)
	}
}
