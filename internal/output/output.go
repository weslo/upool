// Package output holds upool's JSON envelope and exit codes, which mirror the Unity CLI's own
// (docs/contracts.md, section d).
package output

import "encoding/json"

// Exit codes, from docs/contracts.md (d), "upool's own exit codes".
const (
	// ExitOK means the command succeeded.
	ExitOK = 0
	// ExitFailure means the command did not succeed; `upool which` returns it when the working
	// directory is not inside a slot.
	ExitFailure = 1
	// ExitNoTarget mirrors the Unity CLI's exit 6: no Editor instance, or no Unity project, at the
	// resolved target.
	ExitNoTarget = 6
)

// Envelope is the document a command prints with --json, in the Unity CLI's shape.
type Envelope struct {
	Success  bool     `json:"success"`
	Command  string   `json:"command"`
	Data     any      `json:"data"`
	Errors   []Error  `json:"errors"`
	Warnings []string `json:"warnings"`
}

// Error is one entry of Envelope.Errors.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// MarshalJSON writes nil Errors and Warnings as [] rather than null, as the Unity CLI does.
func (e Envelope) MarshalJSON() ([]byte, error) {
	type plain Envelope
	p := plain(e)
	if p.Errors == nil {
		p.Errors = []Error{}
	}
	if p.Warnings == nil {
		p.Warnings = []string{}
	}
	return json.Marshal(p)
}
