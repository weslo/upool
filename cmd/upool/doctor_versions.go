package main

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// The tools whose versions doctor checks, and the lowest version of each that upool works with
// (docs/design.md, "One binary, a dozen commands, one JSON file per pool", the `doctor` row;
// README.md, requirements).
var versionedTools = []struct {
	// check is the check's name, label how a reason names the tool, and command what doctor runs.
	check, label, command string
	minimum               string
	// absent is the reason a tool not on PATH warns with.
	absent string
}{
	{"unity-cli", "the Unity CLI", "unity", "1.0.0-beta.13",
		"unity is not on PATH; upool needs the Unity CLI 1.0.0-beta.13 or later"},
	// The spike's only Claude Code was the desktop app's bundled binary, which is not on PATH
	// (spike §1), so its absence warns rather than fails (#57).
	{"claude-code", "Claude Code", "claude", "2.1.295",
		"claude is not on PATH, so Claude Code's version is not checked; the desktop app " +
			"bundles its own"},
}

// versionTimeout bounds each `--version` call, so that a tool that hangs cannot hang doctor.
const versionTimeout = 10 * time.Second

// semverPattern is a version as both captures print it (testdata/captures/unity/version.raw.txt,
// "1.0.0-beta.13"; testdata/captures/claude/version.txt, "2.1.299"): major.minor.patch and an
// optional prerelease.
var semverPattern = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?$`)

// versionChecks checks each tool's version: it fails below the minimum or when the version cannot
// be read, and warns when the tool is not on PATH. `--version` takes no project, so it is not one
// of the verbs whose targeting docs/contracts.md (c) governs.
func versionChecks(ctx context.Context, inv *invocation) []doctorCheck {
	checks := make([]doctorCheck, 0, len(versionedTools))
	for _, tool := range versionedTools {
		version, found, err := toolVersion(ctx, inv, tool.command)
		switch {
		case err != nil:
			checks = append(checks, failedCheck(tool.check, err))
			continue
		case !found:
			checks = append(checks, doctorCheck{tool.check, checkWarn, tool.absent})
			continue
		}
		below, err := versionBelow(version, tool.minimum)
		switch {
		case err != nil:
			checks = append(checks, failedCheck(tool.check, fmt.Errorf("reading the version "+
				"`%s --version` printed: %w", tool.command, err)))
		case below:
			checks = append(checks, doctorCheck{tool.check, checkFail, fmt.Sprintf("%s is %s, "+
				"below the %s upool needs", tool.label, version, tool.minimum)})
		default:
			checks = append(checks, doctorCheck{tool.check, checkPass, fmt.Sprintf("%s is %s, "+
				"at least %s", tool.label, version, tool.minimum)})
		}
	}
	return checks
}

// toolVersion runs `<command> --version`, found on the invocation's PATH and given its
// environment, and returns the first field of its first line, as both captures put the version
// there. It reports false, and no error, when command is not on PATH.
func toolVersion(ctx context.Context, inv *invocation, command string) (string, bool, error) {
	path, found := lookPath(command, inv.getenv("PATH"))
	if !found {
		return "", false, nil
	}
	ctx, cancel := context.WithTimeout(ctx, versionTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--version")
	cmd.Env = inv.environ()
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			err = fmt.Errorf("no answer within %s", versionTimeout)
		}
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			err = fmt.Errorf("%w: %s", err, msg)
		}
		return "", true, fmt.Errorf("%s --version: %w", path, err)
	}
	line, _, _ := strings.Cut(stdout.String(), "\n")
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return "", true, fmt.Errorf("%s --version printed no version", path)
	}
	return fields[0], true, nil
}

// lookPath finds command in the directories of path, a PATH value, as a shell does: the first
// regular file there that is executable. It reads the invocation's PATH, not this process's, so
// that what doctor runs is what the caller's environment names.
func lookPath(command, path string) (string, bool) {
	for _, dir := range filepath.SplitList(path) {
		if dir == "" {
			continue
		}
		file := filepath.Join(dir, command)
		if info, err := os.Stat(file); err == nil && info.Mode().IsRegular() &&
			info.Mode().Perm()&0o111 != 0 {
			return file, true
		}
	}
	return "", false
}

// versionBelow reports whether version is below minimum, by semantic versioning's precedence: a
// prerelease is below its release, and prerelease identifiers compare field by field, numbers as
// numbers, so beta.12 is below beta.13 and beta.13 below beta.100.
func versionBelow(version, minimum string) (bool, error) {
	v, err := parseSemver(version)
	if err != nil {
		return false, err
	}
	m, err := parseSemver(minimum)
	if err != nil {
		return false, err
	}
	return compareSemver(v, m) < 0, nil
}

// semver is a parsed version: major, minor and patch, and the prerelease's identifiers, none for a
// release.
type semver struct {
	core [3]int
	pre  []string
}

// parseSemver parses a version that semverPattern matches.
func parseSemver(s string) (semver, error) {
	m := semverPattern.FindStringSubmatch(s)
	if m == nil {
		return semver{}, fmt.Errorf("%q is not a version such as 1.0.0 or 1.0.0-beta.13", s)
	}
	var v semver
	for i := range v.core {
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return semver{}, fmt.Errorf("%q: %w", s, err)
		}
		v.core[i] = n
	}
	if m[4] != "" {
		v.pre = strings.Split(m[4], ".")
	}
	return v, nil
}

// compareSemver returns -1, 0 or 1 as a is below, equal to or above b.
func compareSemver(a, b semver) int {
	for i := range a.core {
		if c := cmp.Compare(a.core[i], b.core[i]); c != 0 {
			return c
		}
	}
	switch {
	case len(a.pre) == 0 && len(b.pre) == 0:
		return 0
	case len(a.pre) == 0:
		return 1
	case len(b.pre) == 0:
		return -1
	}
	for i := 0; i < len(a.pre) && i < len(b.pre); i++ {
		if c := compareIdentifiers(a.pre[i], b.pre[i]); c != 0 {
			return c
		}
	}
	return cmp.Compare(len(a.pre), len(b.pre))
}

// compareIdentifiers compares two prerelease identifiers: numbers numerically and below any word,
// words in ASCII order.
func compareIdentifiers(a, b string) int {
	an, aErr := strconv.Atoi(a)
	bn, bErr := strconv.Atoi(b)
	switch {
	case aErr == nil && bErr == nil:
		return cmp.Compare(an, bn)
	case aErr == nil:
		return -1
	case bErr == nil:
		return 1
	}
	return strings.Compare(a, b)
}
