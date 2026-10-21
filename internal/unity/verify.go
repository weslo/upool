package unity

import (
	"context"
	"encoding/json"
)

// Finding is one problem a Unity CLI check found: an entry of data.findings in the replies of
// `unity projects verify` and `unity vcs doctor`
// (testdata/captures/unity/projects-verify-fail.json, vcs-doctor.json).
type Finding struct {
	// Code is the check that found it, such as META_MISSING.
	Code string `json:"code"`
	// Severity is error or warning.
	Severity string `json:"severity"`
	// Path is the file or folder, relative to the project.
	Path    string `json:"path"`
	Message string `json:"message"`
	// Hint says how to fix it.
	Hint string `json:"hint"`
}

// Verify runs `unity projects verify <slot> --strict --format json` through the verb table, the
// check the GUID rule runs before release (docs/design.md, "Lanes: scripts in parallel, scenes one
// at a time, `.meta` never regenerated"), and returns its findings with Reply.Err's error. A slot
// that fails the check is a PROJECTS_VERIFY_FAILED error with the findings that failed it; a call
// that failed for another reason has no findings.
func (c Client) Verify(ctx context.Context, slot string) ([]Finding, error) {
	return c.check(ctx, VerbProjectsVerify, slot, "--strict")
}

// VCSDoctor runs `unity vcs doctor <repo> --format json` through the verb table, check-only: never
// with --fix, which on a clone of the spike's project rewrote .gitattributes and .gitignore and
// installed four git hooks (testdata/captures/unity/vcs-doctor-fix.json; A35). It returns the
// findings with Reply.Err's error: a repository with problems is a VCS_DOCTOR_FAILED error, exit
// 4, with its findings (testdata/captures/unity/vcs-doctor.json); a call that failed for another
// reason has no findings.
func (c Client) VCSDoctor(ctx context.Context, repo string) ([]Finding, error) {
	return c.check(ctx, VerbVCSDoctor, repo)
}

// check runs a verb that reports findings, against the project at path, and returns them with
// Reply.Err's error.
func (c Client) check(ctx context.Context, verb Verb, path string, args ...string) ([]Finding,
	error) {
	reply, err := c.Call(ctx, verb, path, args...)
	if err != nil {
		return nil, err
	}
	var data struct {
		Findings []Finding `json:"findings"`
	}
	// data's shape depends on the reply: a failure with no project to check may carry none, and
	// then there are no findings to report, only the error.
	if json.Unmarshal(reply.Response.Data, &data) != nil {
		data.Findings = nil
	}
	return data.Findings, reply.Err()
}
