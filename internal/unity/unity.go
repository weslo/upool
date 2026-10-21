// Package unity is the only way upool runs the Unity CLI. Every call goes through Client.Call with a
// Verb from the table in verbs.go, which sets UNITY_PROJECT_PATH to the slot and passes the slot
// positionally where docs/contracts.md (c) says (docs/design.md, "Editors: budget by RAM,
// serialize cold imports as a courtesy, target by variable and by path"; "Repository, CI and
// Homebrew distribution": one table, tested row by row against (c)). Response is the CLI's
// --format json reply as upool reads it ((d)).
package unity

import "encoding/json"

// Response is one --format json reply from the Unity CLI. It is deliberately lenient, unlike
// upool's own output.Envelope: the CLI's command field is not a stable verb, data's shape depends on
// the command, some replies (recompile) omit errors and warnings, and a later CLI may add top-level
// fields, which decoding ignores.
type Response struct {
	Success  bool            `json:"success"`
	Command  string          `json:"command"`
	Data     json.RawMessage `json:"data"`
	Errors   []Error         `json:"errors"`
	Warnings []string        `json:"warnings"`
}

// Error is one entry of Response.Errors.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
