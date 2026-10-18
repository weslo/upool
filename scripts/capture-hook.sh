#!/bin/sh
# Records a Claude Code hook's stdin for a manual gate, then hands the same stdin to the hook it
# wraps, if any (testdata/README.md, "Re-capturing").
#
# Register a copy of this script as the hook command, outside ~/Documents:
#
#   /path/to/capture-hook.sh EVENT [HOOK-COMMAND [ARGUMENTS...]]
#
# The desktop app runs WorktreeCreate from a helper that cannot read ~/Documents, so the copy in the
# repository will not run there. Each stdin is saved as ~/.upool-capture/EVENT-<time>-<pid>.json
# (UPOOL_CAPTURE_DIR overrides the folder); replace its paths with scripts/scrubpaths before adding
# it to testdata/captures/hooks/. A WorktreeCreate capture must wrap a command that prints a
# worktree path: with nothing on stdout, the app aborts the session.
set -eu

if [ $# -lt 1 ]; then
	echo "usage: capture-hook.sh EVENT [HOOK-COMMAND [ARGUMENTS...]]" >&2
	exit 2
fi
event=$1
shift

dir=${UPOOL_CAPTURE_DIR:-$HOME/.upool-capture}
mkdir -p "$dir"
file=$dir/$event-$(date +%Y%m%dT%H%M%S)-$$.json
cat >"$file"

if [ $# -gt 0 ]; then
	exec "$@" <"$file"
fi
