// Package assets holds the files upool writes outside its own state, embedded in the binary
// (docs/design.md, "Repository, CI and Homebrew distribution", the layout). They are a package of
// their own because go:embed reaches only files under the embedding package's directory.
//
// They are part of the product, not guidance for agents building upool, and change only through
// an issue (AGENTS.md, "This repository's agent files are not upool's").
package assets

import "embed"

// Hooks holds the hook scripts `upool setup` installs to ~/.upool/hooks, hooks/<event>.sh for
// each event `upool hook` handles (docs/design.md, "Repository, CI and Homebrew distribution":
// "Hook scripts stay trivial on purpose"). Each is POSIX sh, since the desktop app's restricted
// helper runs it (CONTRIBUTING.md, "Scripts"), and ends in `exec HookUpool hook <event>`.
//
//go:embed hooks/*.sh
var Hooks embed.FS

// HookUpool stands in each hook script for the upool it runs. setup replaces it with the path it
// found upool through on PATH, quoted for sh, because PATH under the helper is not the user's.
const HookUpool = "'@UPOOL@'"

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
