package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// projectPlaceholder is the word README.md's quickstart writes for the user's Unity project.
const projectPlaceholder = "<project>"

// readmeQuickstart returns the commands in README.md's quickstart block, the first bash block
// under its Quickstart heading, in order: each line split at && into commands, each command into
// its words, and comments dropped. It fails the test on shell syntax it cannot follow, so that the
// test runs the commands as written or not at all (#105).
func readmeQuickstart(t *testing.T) [][]string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	var cmds [][]string
	section, block := false, false
	for line := range strings.Lines(string(b)) {
		line = strings.TrimRight(line, "\n")
		switch {
		case !block && strings.HasPrefix(line, "## "):
			section = strings.Contains(line, "Quickstart")
		case section && !block && line == "```bash":
			block = true
		case block && line == "```":
			if len(cmds) == 0 {
				t.Fatal("README.md's quickstart block holds no command")
			}
			return cmds
		case block:
			if i := strings.Index(line, "#"); i >= 0 {
				line = line[:i]
			}
			if strings.ContainsAny(line, "'\"`$|;()\\") ||
				strings.ContainsAny(strings.ReplaceAll(line, projectPlaceholder, ""), "<>") {
				t.Fatalf("README.md's quickstart line %q has shell syntax this test cannot run "+
					"as written", line)
			}
			for cmd := range strings.SplitSeq(line, "&&") {
				if words := strings.Fields(cmd); len(words) > 0 {
					cmds = append(cmds, words)
				}
			}
		}
	}
	t.Fatal("README.md has no bash block under a Quickstart heading")
	return nil
}

// README.md's quickstart installs the formula the release publishes: `brew install
// <owner>/<tap>/<name>`, the tap being .goreleaser.yaml's brews repository less its homebrew-
// prefix (docs/design.md, "Repository, CI and Homebrew distribution": the three commands must be
// exactly right).
func TestReadmeQuickstartInstallsTheReleasedFormula(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", ".goreleaser.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var name, owner, repo string
	brews := false
	for line := range strings.Lines(string(b)) {
		field := strings.TrimSpace(line)
		switch {
		case line == "brews:\n":
			brews = true
		case brews && field != "" && !strings.HasPrefix(line, " "):
			brews = false
		case brews && strings.HasPrefix(field, "- name: "):
			name = strings.TrimPrefix(field, "- name: ")
		case brews && strings.HasPrefix(field, "owner: "):
			owner = strings.TrimPrefix(field, "owner: ")
		case brews && strings.HasPrefix(field, "name: "):
			repo = strings.TrimPrefix(field, "name: ")
		}
	}
	tap, ok := strings.CutPrefix(repo, "homebrew-")
	if name == "" || owner == "" || !ok {
		t.Fatalf(".goreleaser.yaml's brews section gives name %q, owner %q and repository %q; "+
			"want all three, the repository named homebrew-<tap>", name, owner, repo)
	}
	want := []string{"brew", "install", owner + "/" + tap + "/" + name}
	var brew [][]string
	for _, cmd := range readmeQuickstart(t) {
		if cmd[0] == "brew" {
			brew = append(brew, cmd)
		}
	}
	if len(brew) != 1 || strings.Join(brew[0], " ") != strings.Join(want, " ") {
		t.Errorf("README.md's quickstart runs brew as %q, want exactly %q", brew, want)
	}
}
