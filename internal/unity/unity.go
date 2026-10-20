// Package unity talks to the Unity CLI. For now it holds Response, the CLI's --format json reply
// as upool reads it (docs/contracts.md, section d), and CacheKey, the one verb upool runs before
// M3's verb table (#82).
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
