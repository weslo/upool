package output

import (
	"fmt"
	"strings"
)

// Timings is how long the steps of an operation that moves a slot or an Editor took, in
// milliseconds: an Envelope's timings and, without --json, one line on stderr (#266;
// docs/contracts.md (d), "upool's own envelope"). internal/timing measures them and appends the
// same fields to the pool's timings log.
type Timings struct {
	// Operation is what ran: take, take --confirm, release, refresh, open or close.
	Operation string `json:"operation"`
	// Slot is the slot the operation acted on, once it found one; refresh, which acts on the
	// pool, names its slots in Steps instead.
	Slot string `json:"slot,omitempty"`
	// Steps are the steps the operation completed, in the order they ran.
	Steps []Step `json:"steps"`
	// TotalMS is the whole operation, steps and everything between them.
	TotalMS int64 `json:"total_ms"`
}

// Step is one step of an operation and how long it took.
type Step struct {
	Name string `json:"name"`
	// Slot is the slot the step acted on, when it is not the operation's own: a slot take
	// cloned, or each slot refresh reset, cloned, moved or re-warmed.
	Slot string `json:"slot,omitempty"`
	MS   int64  `json:"ms"`
}

// Human returns the timings as the line Render prints on stderr, such as
// "timings: take slot-1: clone slot-3 25912 ms, handout 41 ms; total 25960 ms".
func (t Timings) Human() string {
	head := "timings: " + t.Operation
	if t.Slot != "" {
		head += " " + t.Slot
	}
	steps := make([]string, 0, len(t.Steps))
	for _, s := range t.Steps {
		step := s.Name
		if s.Slot != "" {
			step += " " + s.Slot
		}
		steps = append(steps, fmt.Sprintf("%s %d ms", step, s.MS))
	}
	total := fmt.Sprintf("total %d ms", t.TotalMS)
	if len(steps) == 0 {
		return head + ": " + total
	}
	return head + ": " + strings.Join(steps, ", ") + "; " + total
}
