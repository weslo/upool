// Package assets holds the text upool gives a project's sessions, embedded in the binary
// (docs/design.md, "Repository, CI and Homebrew distribution", the layout). They are a package of
// their own because go:embed reaches only files under the embedding package's directory.
//
// They are part of the product, not guidance for agents building upool, and change only through
// an issue (AGENTS.md, "This repository's agent files are not upool's").
package assets

import (
	_ "embed"
	"fmt"
	"strings"
)

// Skill is the upool skill, whose body Instructions carries (docs/design.md, "Hooks route, skills
// teach, leases and lanes keep agents from colliding"). An earlier upool's init installed it in
// the project at .claude/skills/upool/SKILL.md and .agents/skills/upool/SKILL.md, from which init
// and deinit now take it out (A43; #230). Its body is short and operational, Unity's own rules
// restated so that they travel into subagents, which do not inherit an invoked skill.
//
//go:embed skill/upool/SKILL.md
var Skill string

// ClaudeMDBlock is the instruction block that leads Instructions: short and operational, because
// without one agents keep running `git worktree add` (docs/design.md, "Hooks route, skills teach,
// leases and lanes keep agents from colliding"). An earlier upool's init upserted it into a
// project's CLAUDE.md and AGENTS.md, from which init and deinit now take it out (A43; #230). It
// ends in a list item and a newline, so that the item Instructions adds after it, which names the
// pool's base, continues its list.
//
//go:embed claude-md-block.md
var ClaudeMDBlock string

// Branching is what the instructions say about branching, for the reader they go to (#284).
type Branching struct {
	// Base is the pool's base, the branch a slot is handed out detached at (docs/contracts.md (e)
	// `base`; A17), which the main checkout has checked out.
	Base string
	// Detached is whether the reader is a session whose slot is as it was handed out: detached,
	// at a commit base holds. Only such a session is told to make its branch from base. One that
	// has made its branch, or committed on the detached HEAD, would leave its own work behind,
	// and so would a subagent, whose worktree, the slot or one made at the slot's HEAD, is
	// wherever the session has taken it.
	Detached bool
	// OnOrigin is whether the repository has origin/<Base>, which a Detached session branches from
	// after a fetch. A repository with no origin, which A17 allows, has none, and the session
	// branches from Base itself.
	OnOrigin bool
}

// Instructions is what upool tells a session in a slot, and that session's subagents, as hook
// context rather than in a file of the project's (docs/contracts.md A43; #229): ClaudeMDBlock,
// then the item that says what to do about branching (Branching.line), a blank line, then Skill's
// body without its front matter.
func Instructions(b Branching) string {
	body := Skill
	if rest, ok := strings.CutPrefix(body, "---\n"); ok {
		if i := strings.Index(rest, "\n---\n"); i >= 0 {
			body = rest[i+len("\n---\n"):]
		}
	}
	return ClaudeMDBlock + b.line() + "\n" + strings.TrimLeft(body, "\n")
}

// line is the item that follows ClaudeMDBlock's own in Instructions, on one line, since its length
// depends on the base's. No reader may check out base, which git refuses in any worktree but the
// main checkout while that has it checked out: on the first benchmark run, an issue skill's
// `git checkout main` could not run in a slot (#284). A slot is handed out detached at base and
// stays detached once its session is confirmed (A18), so a Detached session is also told to make
// its own branch before it commits: a commit that no branch holds parks the slot when the session
// ends (docs/contracts.md (f), clean; A5).
func (b Branching) line() string {
	if !b.Detached {
		return fmt.Sprintf("- The main checkout has `%[1]s` checked out, so never run "+
			"`git checkout %[1]s` here, even where a workflow says to.\n", b.Base)
	}
	branch := fmt.Sprintf("`git switch -c <name> %s`", b.Base)
	if b.OnOrigin {
		branch = fmt.Sprintf("`git fetch origin`, then `git switch -c <name> origin/%s`", b.Base)
	}
	return fmt.Sprintf("- Your slot starts detached at `%[1]s`, which the main checkout has "+
		"checked out, so never run `git checkout %[1]s` here, even where a workflow says to. "+
		"Before you commit, make your branch with %[2]s: a commit left on the detached HEAD "+
		"parks the slot when the session ends.\n", b.Base, branch)
}

// ClaudeMDBlockVersion is ClaudeMDBlock's version, which the markers of an earlier upool's copy
// carry. internal/block strips a copy of any version, so it need not change with
// claude-md-block.md.
const ClaudeMDBlockVersion = 1
