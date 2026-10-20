package main

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/weslo/upool/internal/output"
	"github.com/weslo/upool/internal/unity/fake"
)

// doctor reads the Unity CLI's and Claude Code's versions from the captured replies, replayed by a
// fake unity and a fake claude on PATH, and from copies of them in which the test substitutes a
// lower version (#57). Each case puts on PATH only the fakes it is about, since each run of one
// is a run of this test binary; a tool left off warns.
func TestDoctorVersions(t *testing.T) {
	unity := func(t *testing.T, version string, exit int) *fake.Reply {
		r := capturedVersion(t, "unity", "version.raw.txt")
		if version != "" {
			r.Replace = []string{"1.0.0-beta.13", version}
		}
		r.Exit += exit
		return &r
	}
	claude := func(t *testing.T, version string) *fake.Reply {
		r := capturedVersion(t, "claude", "version.txt")
		if version != "" {
			r.Replace = []string{"2.1.299", version}
		}
		return &r
	}
	type want struct {
		result checkResult
		reason string // the reason, or its start or end
	}
	tests := []struct {
		name          string
		unity, claude func(t *testing.T) *fake.Reply // nil leaves the tool off PATH
		wantUnity     want
		wantClaude    want
	}{
		{"the captured versions",
			func(t *testing.T) *fake.Reply { return unity(t, "", 0) },
			func(t *testing.T) *fake.Reply { return claude(t, "") },
			want{checkPass, "the Unity CLI is 1.0.0-beta.13, at least 1.0.0-beta.13"},
			want{checkPass, "Claude Code is 2.1.299, at least 2.1.295"}},
		{"a Unity CLI below the minimum",
			func(t *testing.T) *fake.Reply { return unity(t, "1.0.0-beta.12", 0) }, nil,
			want{checkFail, "the Unity CLI is 1.0.0-beta.12, below the 1.0.0-beta.13 upool needs"},
			want{checkWarn, "claude is not on PATH"}},
		{"a Claude Code below the minimum",
			nil, func(t *testing.T) *fake.Reply { return claude(t, "2.1.294") },
			want{checkWarn, "unity is not on PATH"},
			want{checkFail, "Claude Code is 2.1.294, below the 2.1.295 upool needs"}},
		{"a version that cannot be read",
			func(t *testing.T) *fake.Reply { return unity(t, "beta", 0) }, nil,
			want{checkFail, "reading the version `unity --version` printed: \"beta\" is not a " +
				"version such as 1.0.0 or 1.0.0-beta.13"},
			want{checkWarn, "claude is not on PATH"}},
		{"a failing --version",
			func(t *testing.T) *fake.Reply { return unity(t, "", 2) }, nil,
			want{checkFail, "unity --version: exit status 2"},
			want{checkWarn, "claude is not on PATH"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newHarnessPool(t)
			dirs := []string{p.Getenv("PATH")} // git alone
			var fakes []*fake.Unity
			if tt.unity != nil {
				u := fake.Install(t, *tt.unity(t))
				dirs, fakes = append([]string{u.Dir()}, dirs...), append(fakes, u)
			}
			if tt.claude != nil {
				c := fake.InstallClaude(t, *tt.claude(t))
				dirs, fakes = append([]string{c.Dir()}, dirs...), append(fakes, c)
			}
			p.Setenv("PATH", strings.Join(dirs, string(os.PathListSeparator)))

			r := runIn(t, p.Env, p.Repo, "doctor", "--json")
			wantExit := output.ExitOK
			if tt.wantUnity.result == checkFail || tt.wantClaude.result == checkFail {
				wantExit = output.ExitFailure
			}
			if r.code != wantExit {
				t.Errorf("exit = %d, want %d", r.code, wantExit)
			}
			checks := doctorChecks(t, r)
			for name, w := range map[string]want{"unity-cli": tt.wantUnity,
				"claude-code": tt.wantClaude} {
				c := findCheck(t, checks, name)
				if c.Result != w.result || !strings.HasSuffix(c.Reason, w.reason) &&
					!strings.HasPrefix(c.Reason, w.reason) {
					t.Errorf("%s: %s, %q; want %s, %q", name, c.Result, c.Reason, w.result,
						w.reason)
				}
			}
			for _, f := range fakes {
				if calls := f.Calls(); len(calls) != 1 ||
					!slices.Equal(calls[0].Args, []string{"--version"}) {
					t.Errorf("calls = %+v, want one of --version", calls)
				}
			}
		})
	}
}

// Versions compare by semantic versioning's precedence, the prerelease field by field.
func TestDoctorVersionsCompareByPrecedence(t *testing.T) {
	tests := []struct {
		version, minimum string
		below            bool
	}{
		{"1.0.0-beta.13", "1.0.0-beta.13", false},
		{"1.0.0-beta.12", "1.0.0-beta.13", true},
		{"1.0.0-beta.100", "1.0.0-beta.13", false},
		{"1.0.0-beta", "1.0.0-beta.13", true},
		{"1.0.0-alpha.20", "1.0.0-beta.13", true},
		{"1.0.0-rc.1", "1.0.0-beta.13", false},
		{"1.0.0", "1.0.0-beta.13", false},
		{"0.9.9", "1.0.0-beta.13", true},
		{"2.1.299", "2.1.295", false},
		{"2.1.294", "2.1.295", true},
		{"2.1.1000", "2.1.295", false},
		{"2.2.0", "2.1.295", false},
		{"10.0.0", "2.1.295", false},
		{"2.1.295-beta.1", "2.1.295", true},
	}
	for _, tt := range tests {
		below, err := versionBelow(tt.version, tt.minimum)
		if err != nil || below != tt.below {
			t.Errorf("versionBelow(%s, %s) = %t, %v; want %t", tt.version, tt.minimum, below,
				err, tt.below)
		}
	}
	for _, bad := range []string{"beta", "1.0", "v1.0.0", "1.0.0-", ""} {
		if _, err := versionBelow(bad, "1.0.0"); err == nil {
			t.Errorf("versionBelow(%q) succeeded, want it refused as not a version", bad)
		}
	}
}

// Neither tool on PATH warns, and warnings alone exit 0: the spike's Claude Code was the desktop
// app's bundled binary, never on PATH.
func TestDoctorVersionsWarnWhenNeitherToolIsOnPATH(t *testing.T) {
	p := newHarnessPool(t) // with only git on PATH
	r := runIn(t, p.Env, p.Repo, "doctor", "--json")
	if r.code != output.ExitOK {
		t.Errorf("exit = %d, want %d", r.code, output.ExitOK)
	}
	checks := doctorChecks(t, r)
	for name, want := range map[string]string{
		"unity-cli": "unity is not on PATH; upool needs the Unity CLI 1.0.0-beta.13 or later",
		"claude-code": "claude is not on PATH, so Claude Code's version is not checked; the " +
			"desktop app bundles its own",
	} {
		if c := findCheck(t, checks, name); c.Result != checkWarn || c.Reason != want {
			t.Errorf("%s: %s, %q; want warn, %q", name, c.Result, c.Reason, want)
		}
	}
}
