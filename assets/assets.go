// Package assets holds the text upool gives a project's sessions, embedded in the binary
// (docs/design.md, "Repository, CI and Homebrew distribution", the layout). They are a package of
// their own because go:embed reaches only files under the embedding package's directory.
//
// They are part of the product, not guidance for agents building upool, and change only through
// an issue (AGENTS.md, "This repository's agent files are not upool's").
package assets

import (
	_ "embed"
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
// ends in a newline.
//
//go:embed claude-md-block.md
var ClaudeMDBlock string

// Instructions is what upool tells a session in a slot, and that session's subagents, as hook
// context rather than in a file of the project's (docs/contracts.md A43; #229): ClaudeMDBlock, a
// blank line, then Skill's body without its front matter.
func Instructions() string {
	body := Skill
	if rest, ok := strings.CutPrefix(body, "---\n"); ok {
		if i := strings.Index(rest, "\n---\n"); i >= 0 {
			body = rest[i+len("\n---\n"):]
		}
	}
	return ClaudeMDBlock + "\n" + strings.TrimLeft(body, "\n")
}

// ClaudeMDBlockVersion is ClaudeMDBlock's version, which the markers of an earlier upool's copy
// carry. internal/block strips a copy of any version, so it need not change with
// claude-md-block.md.
const ClaudeMDBlockVersion = 1
