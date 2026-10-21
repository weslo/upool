package unity

import (
	"context"
	"encoding/json"
)

// Finding is one problem `unity projects verify` found: an entry of its reply's data.findings
// (testdata/captures/unity/projects-verify-fail.json).
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
	reply, err := c.Call(ctx, VerbProjectsVerify, slot, "--strict")
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
