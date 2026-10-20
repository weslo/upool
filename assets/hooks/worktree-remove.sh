#!/bin/sh
# Claude Code's WorktreeRemove hook, installed by `upool setup`, which rewrites it on every run.
# It runs upool by the path setup found it through on PATH: PATH here is not the user's.
set -eu
exec '@UPOOL@' hook worktree-remove
