// Package assets holds the files upool writes outside its own state, embedded in the binary
// (docs/design.md, "Repository, CI and Homebrew distribution", the layout). They are a package of
// their own because go:embed reaches only files under the embedding package's directory.
//
// They are part of the product, not guidance for agents building upool, and change only through
// an issue (AGENTS.md, "This repository's agent files are not upool's").
package assets

import _ "embed" // for go:embed

// ClaudeMDBlock is the instruction block `upool init` upserts into a project's CLAUDE.md
// (docs/design.md, "One binary, a dozen commands, one JSON file per pool", the `init` row): short
// and operational, because without one agents keep running `git worktree add` (docs/design.md,
// "Hooks route, skills teach, leases and lanes keep agents from colliding"). It ends in a newline.
//
//go:embed claude-md-block.md
var ClaudeMDBlock string

// ClaudeMDBlockVersion is ClaudeMDBlock's version, which the block's markers carry. Raise it
// whenever claude-md-block.md changes: an upsert replaces a copy of an older version and keeps
// one of a newer version (internal/block).
const ClaudeMDBlockVersion = 1
