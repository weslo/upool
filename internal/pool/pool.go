// Package pool is a repository's pool of slots: the ~/.upool/pools/<repo-key>/state.json document
// with its slots, their leases, the agent worktrees the create hook made and the one refresh job
// (docs/contracts.md (e) State schemas), and docs/contracts.md (f) Slot lifecycle: the slot states,
// and Next, the transitions between them as a pure function.
//
// The keys are exactly the snake_case names in (e), in its order, and the types its JSON types
// (A7). Timestamps are written as RFC 3339 strings in UTC; a process start time keeps the
// microseconds internal/proc reads, so it compares equal after a round trip (A7). The document is
// read and written only through internal/statefile, under ~/.upool/lock.
//
// The home directory is always a parameter; nothing here reads the environment (CONTRIBUTING.md,
// "Process state comes in through run").
package pool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"time"

	"github.com/weslo/upool/internal/statefile"
)

// SchemaVersion is the state.json format this package reads and writes: a document with a higher
// schema_version is refused, and one with a lower version is written at this one (A7).
const SchemaVersion = 1

// State is a slot's lifecycle state, one of the states in docs/contracts.md (f).
type State string

// The slot states of docs/contracts.md (f). Every one is persisted: creating and warming are the
// states that keep a slot out of circulation while the lock is dropped (A4).
const (
	// StateCreating is a slot being cloned, written before `git worktree add` with the cloning
	// process in owner_pid: the "acquisition incomplete" entry (A4).
	StateCreating State = "creating"
	// StateWarming is a ready slot out of circulation, with the process holding it in owner_pid,
	// while refresh moves it to base and re-warms it or a clone copies it as its seed (A4; A19).
	StateWarming State = "warming"
	// StateReady is detached at base, clean and unleased.
	StateReady State = "ready"
	// StateLeasedUnconfirmed is leased by the create hook, with confirmed false.
	StateLeasedUnconfirmed State = "leased-unconfirmed"
	// StateLeased is leased and confirmed.
	StateLeased State = "leased"
	// StateParked is dirty after a remove or release: unleased, not ready, never reset.
	StateParked State = "parked"
	// StateNeedsReset is clean after a remove, with its reset deferred to the confirm step of its
	// next lease or to refresh (A5).
	StateNeedsReset State = "needs-reset"
)

// Pool is the state.json document.
type Pool struct {
	SchemaVersion int `json:"schema_version"`
	// Repo is the main checkout's absolute path.
	Repo string `json:"repo"`
	// Base is the git ref slots are cloned at and reset to (A17).
	Base  string `json:"base"`
	Slots []Slot `json:"slots"`
	// AgentWorktrees are the subagent worktrees the create hook made, which are not slots.
	AgentWorktrees []AgentWorktree `json:"agent_worktrees"`
	// RefreshJob is the one detached top-up job, or nil when none has been recorded.
	RefreshJob *RefreshJob `json:"refresh_job"`
}

// Slot is one slot, <repo>/.claude/worktrees/slot-N.
type Slot struct {
	// Name is slot-N.
	Name string `json:"name"`
	// Path is the slot's absolute path.
	Path  string `json:"path"`
	State State  `json:"state"`
	// OwnerPID is the process cloning the slot (creating) or re-warming it (warming), and nil in
	// every other state (A4).
	OwnerPID *int `json:"owner_pid"`
	// OwnerPIDStart is that process's start time from internal/proc, so a reused PID is not
	// mistaken for the owner, and nil when OwnerPID is.
	OwnerPIDStart *time.Time `json:"owner_pid_start"`
	// SeedHead is the seed source's HEAD at clone time: the commit the cloned Library matches,
	// which may differ from the slot's own HEAD (A10).
	SeedHead string `json:"seed_head"`
	// SeedDirty is whether the seed source had uncommitted changes, which the clone reverts.
	SeedDirty bool `json:"seed_dirty"`
	// ReimportPending is set when a checkout upool made in the slot changed `unity cache key`, so
	// the next open is charged as a cold import (A20).
	ReimportPending bool `json:"reimport_pending"`
	// LastUsed drives LRU removal under max_disk and shrink_after.
	LastUsed time.Time `json:"last_used"`
	// Lease is the current lease, or nil.
	Lease *Lease `json:"lease"`
}

// The lanes a lease is in (docs/design.md, "Lanes: scripts in parallel, scenes one at a time,
// `.meta` never regenerated"): LaneScript, the default, for any number of leases, and LaneScene,
// held by one lease of a project at a time.
const (
	LaneScript = "script"
	LaneScene  = "scene"
)

// Lease is a slot's current lease: this entry alone, with no `git worktree lock` (docs/contracts.md
// (e); A27).
type Lease struct {
	// LeaseID is random, the identity `release --if-lease-id` checks.
	LeaseID string `json:"lease_id"`
	// Holder is --holder, or for the desktop hook the create stdin's session_id.
	Holder string `json:"holder"`
	// WorktreePath correlates `take --confirm`, the remove hook and release with the lease.
	WorktreePath string `json:"worktree_path"`
	// HolderPID is the process whose life the lease follows, for dead-PID reclaim; nil for a bare
	// take, which is never reclaimed automatically, and for a create-hook lease until confirmed
	// (A6).
	HolderPID *int `json:"holder_pid"`
	// HolderPIDStart is that process's start time from internal/proc, and nil when HolderPID is.
	HolderPIDStart *time.Time `json:"holder_pid_start"`
	// AcquiredAt drives confirm_timeout, and doctor's warning about a lease with no holder PID.
	AcquiredAt time.Time `json:"acquired_at"`
	// Confirmed is false only for a lease the create hook takes, until `take --confirm` (A6).
	Confirmed bool `json:"confirmed"`
	// ResetPending is true for a create-hook lease on a slot handed out from needs-reset
	// (transition 10), whose deferred reset has not run: `take --confirm` runs it, and an expiry
	// before then returns the slot to needs-reset rather than ready (A23).
	ResetPending bool `json:"reset_pending"`
	// Lane is LaneScript or LaneScene; a project has at most one scene lease.
	Lane string `json:"lane"`
	// NameHint is the create stdin's name, kept for display and never used as a key.
	NameHint string `json:"name_hint"`
}

// AgentWorktree is a subagent worktree the create hook made, <repo>/.claude/worktrees/agent-<hex>.
type AgentWorktree struct {
	Path string `json:"path"`
	// ParentSessionID is the create stdin's session_id.
	ParentSessionID string `json:"parent_session_id"`
	// ParentPID is the creating process's parent, the session's CLI.
	ParentPID int `json:"parent_pid"`
	// CreatedAt drives agent_ttl.
	CreatedAt time.Time `json:"created_at"`
}

// RefreshJob is the detached `upool refresh` that tops the pool up after a handout, recorded so
// that status can announce it while it runs. Its keys, pid and pid_start, were settled on #38
// (docs/contracts.md (e); A7).
type RefreshJob struct {
	PID int `json:"pid"`
	// PIDStart is the job's start time from internal/proc, so a reused PID is not mistaken for it.
	PIDStart time.Time `json:"pid_start"`
}

// Load returns the pool in ~/.upool/pools/<repoKey>/state.json under home, without the lock (see
// statefile.Read). A missing file is the zero Pool, which has no Repo.
func Load(home, repoKey string) (Pool, error) {
	file, shown, err := stateFile(repoKey)
	if err != nil {
		return Pool{}, err
	}
	data, err := statefile.Read(home, file)
	if err != nil {
		return Pool{}, err
	}
	return decode(data, shown)
}

// Update rewrites ~/.upool/pools/<repoKey>/state.json under home under the lock: fn gets the pool
// as it is (the zero Pool when the file is missing) and changes it in place, and the result is
// written at SchemaVersion. When fn returns an error, Update returns it as it is and writes
// nothing. Waiting for the lock gives up when ctx ends, as statefile.Update does.
//
// fn runs while ~/.upool/lock is held, so it must not call anything that takes the lock, such as
// another Update.
func Update(ctx context.Context, home, repoKey string, fn func(*Pool) error) error {
	file, shown, err := stateFile(repoKey)
	if err != nil {
		return err
	}
	return statefile.Update(ctx, home, file, func(old []byte) ([]byte, error) {
		p, err := decode(old, shown)
		if err != nil {
			return nil, err
		}
		if err := fn(&p); err != nil {
			return nil, err
		}
		return encode(p, shown)
	})
}

// stateFile returns state.json's path relative to ~/.upool, as internal/statefile takes it, and
// how messages name it. repoKey must be one path element, as registry.Key makes it, so that a pool
// never writes outside its own directory.
func stateFile(repoKey string) (file, shown string, err error) {
	if repoKey == "" || repoKey == "." || repoKey == ".." || filepath.Base(repoKey) != repoKey {
		return "", "", fmt.Errorf("repo key %q is not a single path element", repoKey)
	}
	return filepath.Join("pools", repoKey, "state.json"),
		"~/.upool/pools/" + repoKey + "/state.json", nil
}

// decode parses a state.json document. Empty data, a missing file, is the zero Pool.
//
// The version is read first, leniently, so that a newer document is refused with a message to
// upgrade rather than an error about the keys it added (A7). The document is then decoded with
// unknown keys as errors, so a rewrite never silently drops a field this upool does not know.
func decode(data []byte, shown string) (Pool, error) {
	if len(data) == 0 {
		return Pool{}, nil
	}
	var version struct {
		SchemaVersion int `json:"schema_version"`
	}
	// Unmarshal also rejects anything after the document, which the Decoder below would not.
	if err := json.Unmarshal(data, &version); err != nil {
		return Pool{}, fmt.Errorf("decoding %s: %w", shown, err)
	}
	if version.SchemaVersion > SchemaVersion {
		return Pool{}, fmt.Errorf("%s has schema_version %d, newer than the %d this upool "+
			"reads: upgrade upool", shown, version.SchemaVersion, SchemaVersion)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var p Pool
	if err := dec.Decode(&p); err != nil {
		return Pool{}, fmt.Errorf("decoding %s: %w", shown, err)
	}
	return p, nil
}

// encode writes p at SchemaVersion, indented, with a final newline.
func encode(p Pool, shown string) ([]byte, error) {
	data, err := json.MarshalIndent(normalize(p), "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encoding %s: %w", shown, err)
	}
	return append(data, '\n'), nil
}

// normalize returns p as it is written: at SchemaVersion, with every timestamp in UTC and every
// array an array rather than null (docs/contracts.md (e)). It copies what it changes, so p's
// slices and pointers are left as they were.
func normalize(p Pool) Pool {
	p.SchemaVersion = SchemaVersion
	p.Slots = slices.Clone(p.Slots)
	if p.Slots == nil {
		p.Slots = []Slot{}
	}
	for i := range p.Slots {
		s := &p.Slots[i]
		s.OwnerPIDStart = utc(s.OwnerPIDStart)
		s.LastUsed = s.LastUsed.UTC()
		if s.Lease != nil {
			l := *s.Lease
			l.HolderPIDStart = utc(l.HolderPIDStart)
			l.AcquiredAt = l.AcquiredAt.UTC()
			s.Lease = &l
		}
	}
	p.AgentWorktrees = slices.Clone(p.AgentWorktrees)
	if p.AgentWorktrees == nil {
		p.AgentWorktrees = []AgentWorktree{}
	}
	for i := range p.AgentWorktrees {
		p.AgentWorktrees[i].CreatedAt = p.AgentWorktrees[i].CreatedAt.UTC()
	}
	if p.RefreshJob != nil {
		j := *p.RefreshJob
		j.PIDStart = j.PIDStart.UTC()
		p.RefreshJob = &j
	}
	return p
}

// utc returns a new pointer to *t in UTC, or nil when t is nil.
func utc(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}
