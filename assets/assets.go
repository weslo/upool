// Package assets holds the files upool writes outside its own state, embedded in the binary
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

// Skill is the upool skill, which `upool init` installs in the project at
// .claude/skills/upool/SKILL.md and .agents/skills/upool/SKILL.md (A26; docs/design.md, "Hooks
// route, skills teach, leases and lanes keep agents from colliding"). Its body is short and
// operational, Unity's own rules restated so that they travel into subagents, which do not
// inherit an invoked skill.
//
//go:embed skill/upool/SKILL.md
var Skill string

// ClaudeMDBlock is the instruction block `upool init` upserts into a project's CLAUDE.md
// (docs/design.md, "One binary, a dozen commands, one JSON file per pool", the `init` row): short
// and operational, because without one agents keep running `git worktree add` (docs/design.md,
// "Hooks route, skills teach, leases and lanes keep agents from colliding"). It ends in a newline.
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

// ClaudeMDBlockVersion is ClaudeMDBlock's version, which the block's markers carry. Raise it
// whenever claude-md-block.md changes: an upsert replaces a copy of an older version and keeps
// one of a newer version (internal/block).
const ClaudeMDBlockVersion = 1
