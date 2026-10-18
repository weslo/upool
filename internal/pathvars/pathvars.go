// Package pathvars swaps machine paths for the variables testdata/README.md defines, and back: Fill
// writes a test's paths into a capture, and Scrub takes a machine's paths out of a new capture.
package pathvars

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Var names one path. Each Var gives three variables: ${Name} for the path, ${Name_key} for Claude
// Code's project key of it, and ${Name_lc} for it lowercased.
type Var struct {
	Name string
	Path string
}

// Key is Claude Code's project key for path: every "/" and "." replaced by "-".
func Key(path string) string {
	return strings.NewReplacer("/", "-", ".", "-").Replace(path)
}

type form struct{ variable, value string }

func (v Var) forms() []form {
	return []form{
		{"${" + v.Name + "}", v.Path},
		{"${" + v.Name + "_key}", Key(v.Path)},
		{"${" + v.Name + "_lc}", strings.ToLower(v.Path)},
	}
}

var variable = regexp.MustCompile(`\$\{[a-z_]+\}`)

// Fill replaces every variable that vars define with its value, by literal replacement, so a
// literal "$schema" in a capture is left alone. It fails if a ${…} variable remains, so a test
// cannot leave one unfilled by accident.
func Fill(text string, vars ...Var) (string, error) {
	var pairs []string
	for _, v := range vars {
		for _, f := range v.forms() {
			pairs = append(pairs, f.variable, f.value)
		}
	}
	out := strings.NewReplacer(pairs...).Replace(text)
	if left := variable.FindAllString(out, -1); len(left) > 0 {
		return "", fmt.Errorf("unfilled variables: %s", strings.Join(unique(left), ", "))
	}
	return out, nil
}

// machinePath matches what `make check-paths` rejects.
var machinePath = regexp.MustCompile(`/([Uu]sers|home)/[A-Za-z0-9._-]+|-Users-[A-Za-z0-9._]+-|/private/tmp/claude-[0-9]+|/var/folders/[A-Za-z0-9_]+`)

// Scrub replaces each Var's path, project key and lowercased path with its variable. The longest
// match wins, so a repository under the home directory becomes ${repo}, not ${home}/…; when two
// Vars give the same text, the earlier one wins. Scrub fails if anything `make check-paths` rejects
// is left.
func Scrub(text string, vars ...Var) (string, error) {
	var forms []form
	seen := map[string]bool{}
	for _, v := range vars {
		for _, f := range v.forms() {
			if f.value == "" || seen[f.value] {
				continue
			}
			seen[f.value] = true
			forms = append(forms, f)
		}
	}
	sort.SliceStable(forms, func(i, j int) bool { return len(forms[i].value) > len(forms[j].value) })
	// strings.Replacer tries its pairs in order at each position, so longest first means longest match.
	pairs := make([]string, 0, 2*len(forms))
	for _, f := range forms {
		pairs = append(pairs, f.value, f.variable)
	}
	out := strings.NewReplacer(pairs...).Replace(text)
	if left := machinePath.FindAllString(out, -1); len(left) > 0 {
		return "", fmt.Errorf("machine paths left after scrubbing: %s", strings.Join(unique(left), ", "))
	}
	return out, nil
}

func unique(s []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range s {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}
