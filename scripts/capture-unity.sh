#!/usr/bin/env bash
# Records one Unity CLI reply as a new capture (testdata/README.md, "Re-capturing"). Run it in the
# state the capture needs: which Editors are open, and from which directory.
#
#   scripts/capture-unity.sh [-o DIR] [--var name=path]... NAME UNITY-ARGUMENTS...
#
# It runs `unity UNITY-ARGUMENTS...` (include --format json) and writes, with machine paths
# replaced by variables:
#
#   DIR/NAME.json         stdout, pretty-printed with jq (NAME.raw.txt when stdout is not JSON)
#   DIR/NAME.stderr.txt   stderr, when there is any
#   DIR/NAME.exit         the exit code
#
# DIR defaults to testdata/captures/unity; point it at a scratch directory to diff a re-capture
# against the committed one. The working directory is the cwd variable, after any --var flags, and
# scripts/scrubpaths adds home and claude_tmp. It refuses to overwrite a file: captures are never
# edited.
set -euo pipefail

usage() {
	echo "usage: scripts/capture-unity.sh [-o DIR] [--var name=path]... NAME UNITY-ARGUMENTS..." >&2
	exit 2
}

root=$(cd "$(dirname "$0")/.." && pwd -P)
out=$root/testdata/captures/unity
vars=()
while [[ $# -gt 0 ]]; do
	case $1 in
	-o) [[ $# -ge 2 ]] || usage; out=$2; shift 2 ;;
	--var) [[ $# -ge 2 ]] || usage; vars+=(--var "$2"); shift 2 ;;
	-*) usage ;;
	*) break ;;
	esac
done
[[ $# -ge 2 ]] || usage
name=$1
shift
vars+=(--var "cwd=$(pwd -P)")

for f in "$out/$name.json" "$out/$name.raw.txt" "$out/$name.stderr.txt" "$out/$name.exit"; do
	if [[ -e $f ]]; then
		echo "capture-unity: $f exists; captures are never overwritten" >&2
		exit 1
	fi
done

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
(cd "$root" && go build -o "$tmp/scrubpaths" ./scripts/scrubpaths)

code=0
unity "$@" >"$tmp/stdout" 2>"$tmp/stderr" || code=$?

# Scrub everything before writing anything, so a failed scrub leaves no partial capture.
if [[ ! -s $tmp/stdout ]]; then
	stdout_file=
elif jq . "$tmp/stdout" >"$tmp/pretty" 2>/dev/null; then
	stdout_file=$name.json
	"$tmp/scrubpaths" "${vars[@]}" <"$tmp/pretty" >"$tmp/stdout.scrubbed"
else
	stdout_file=$name.raw.txt
	"$tmp/scrubpaths" "${vars[@]}" <"$tmp/stdout" >"$tmp/stdout.scrubbed"
fi
if [[ -s $tmp/stderr ]]; then
	"$tmp/scrubpaths" "${vars[@]}" <"$tmp/stderr" >"$tmp/stderr.scrubbed"
fi

mkdir -p "$out"
if [[ -n $stdout_file ]]; then
	mv "$tmp/stdout.scrubbed" "$out/$stdout_file"
fi
if [[ -s $tmp/stderr ]]; then
	mv "$tmp/stderr.scrubbed" "$out/$name.stderr.txt"
fi
echo "$code" >"$out/$name.exit"
echo "capture-unity: unity exited $code; wrote ${stdout_file:-no stdout}$([[ -s $tmp/stderr ]] && echo ", $name.stderr.txt"), $name.exit in $out" >&2
