package clone

import "context"

// Copier copies one top-level entry of a seed into a slot, the way docs/contracts.md (g) Clone
// recipe, step 4, does: `cp -cR <src> <dst>`, which on APFS clones each file rather than copying
// its bytes, keeps its mtime, and copies a symbolic link as a link (S§2 TA; S§9 E.1). dst must
// not exist yet.
type Copier interface {
	Copy(ctx context.Context, src, dst string) error
}

// CP is the Copier that runs /bin/cp -cR. Off darwin its Copy is an error, since `-c` is macOS's
// clonefile(2) and Linux waits until the macOS core is dogfooded (docs/design.md, "Build in four
// phases, with the clone recipe first", the leave-out list).
type CP struct{}

var _ Copier = CP{}
