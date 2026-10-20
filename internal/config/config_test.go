package config

import (
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// tempDir is t.TempDir with its symlinks resolved (on macOS it is under /var, which is
// /private/var).
func tempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// writeUser writes doc as the user file under home.
func writeUser(t *testing.T, home, doc string) {
	t.Helper()
	path := UserPath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeRepo writes doc as the repo file of repo.
func writeRepo(t *testing.T, repo, doc string) {
	t.Helper()
	if err := os.WriteFile(RepoPath(repo), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
}

// env returns a Getenv that knows only vars.
func env(vars map[string]string) func(string) string {
	return func(name string) string { return vars[name] }
}

// ram64 is Inputs.Memory for a 64 GB machine, the one docs/contracts.md (h) works through.
func ram64() (uint64, error) {
	return 64 << 30, nil
}

// load is Load with a 64 GB machine unless in says otherwise, failing the test on an error.
func load(t *testing.T, in Inputs) Config {
	t.Helper()
	if in.Memory == nil {
		in.Memory = ram64
	}
	c, err := Load(in)
	if err != nil {
		t.Fatalf("Load error = %v, want nil", err)
	}
	return c
}

// loadErr is Load with a 64 GB machine, returning its error, which the test wants.
func loadErr(t *testing.T, in Inputs) error {
	t.Helper()
	in.Memory = ram64
	_, err := Load(in)
	if err == nil {
		t.Fatal("Load error = nil, want an error")
	}
	return err
}

// sources is every setting's source, by key.
func sources(c Config) map[string]Source {
	return map[string]Source{
		"ready":                c.Ready.Source,
		"max_slots":            c.MaxSlots.Source,
		"base":                 c.Base.Source,
		"max_cold_imports":     c.MaxColdImports.Source,
		"editor_ram_budget_gb": c.EditorRAMBudgetGB.Source,
		"editor_ram_gb":        c.EditorRAMGB.Source,
		"helper_ram_gb":        c.HelperRAMGB.Source,
		"max_editors":          c.MaxEditors.Source,
		"shrink_after":         c.ShrinkAfter.Source,
		"agent_ttl":            c.AgentTTL.Source,
		"confirm_timeout":      c.ConfirmTimeout.Source,
	}
}

// nonDefault is a value for every key, as a flag or variable would give it, that is not its
// default; checkNonDefault checks a Config holds exactly these.
var nonDefault = map[string]string{
	"ready":                "2",
	"max_slots":            "5",
	"base":                 "develop",
	"max_cold_imports":     "2",
	"editor_ram_budget_gb": "20",
	"editor_ram_gb":        "3",
	"helper_ram_gb":        "1.5",
	"max_editors":          "4",
	"shrink_after":         "7d",
	"agent_ttl":            "12h",
	"confirm_timeout":      "90s",
}

func checkNonDefault(t *testing.T, c Config) {
	t.Helper()
	if c.Ready.Value != 2 || c.MaxSlots.Value != 5 || c.Base.Value != "develop" ||
		c.MaxColdImports.Value != 2 || c.MaxEditors.Value != 4 {
		t.Errorf("ready, max_slots, base, max_cold_imports, max_editors = %d, %d, %q, %d, %d, "+
			"want 2, 5, \"develop\", 2, 4", c.Ready.Value, c.MaxSlots.Value, c.Base.Value,
			c.MaxColdImports.Value, c.MaxEditors.Value)
	}
	if c.EditorRAMBudgetGB.Value != 20 || c.EditorRAMGB.Value != 3 || c.HelperRAMGB.Value != 1.5 {
		t.Errorf("editor_ram_budget_gb, editor_ram_gb, helper_ram_gb = %v, %v, %v, want 20, 3, 1.5",
			c.EditorRAMBudgetGB.Value, c.EditorRAMGB.Value, c.HelperRAMGB.Value)
	}
	if c.ShrinkAfter.Value != 7*24*time.Hour || c.AgentTTL.Value != 12*time.Hour ||
		c.ConfirmTimeout.Value != 90*time.Second {
		t.Errorf("shrink_after, agent_ttl, confirm_timeout = %v, %v, %v, want 168h, 12h, 90s",
			c.ShrinkAfter.Value, c.AgentTTL.Value, c.ConfirmTimeout.Value)
	}
}

func TestKeysAreConfigsSettings(t *testing.T) {
	got := slices.Sorted(slices.Values(keys))
	want := slices.Sorted(maps.Keys(sources(Config{})))
	if !slices.Equal(got, want) {
		t.Errorf("keys = %q, want %q", got, want)
	}
}

func TestDefaultsWhenNothingSetsAnything(t *testing.T) {
	c := load(t, Inputs{Home: tempDir(t), Repo: tempDir(t)})
	if c.Ready != (Setting[int]{1, Default}) {
		t.Errorf("ready = %+v, want 1 from default", c.Ready)
	}
	if c.MaxSlots != (Setting[int]{3, Default}) {
		t.Errorf("max_slots = %+v, want 3 from default", c.MaxSlots)
	}
	if c.MaxColdImports != (Setting[int]{1, Default}) {
		t.Errorf("max_cold_imports = %+v, want 1 from default", c.MaxColdImports)
	}
	if c.EditorRAMBudgetGB != (Setting[float64]{16, Default}) {
		t.Errorf("editor_ram_budget_gb = %+v, want 16 from default", c.EditorRAMBudgetGB)
	}
	if c.EditorRAMGB != (Setting[float64]{2.5, Default}) {
		t.Errorf("editor_ram_gb = %+v, want 2.5 from default", c.EditorRAMGB)
	}
	if c.HelperRAMGB != (Setting[float64]{2, Default}) {
		t.Errorf("helper_ram_gb = %+v, want 2 from default", c.HelperRAMGB)
	}
	if c.ShrinkAfter != (Setting[time.Duration]{14 * 24 * time.Hour, Default}) {
		t.Errorf("shrink_after = %+v, want 336h from default", c.ShrinkAfter)
	}
	if c.AgentTTL != (Setting[time.Duration]{24 * time.Hour, Default}) {
		t.Errorf("agent_ttl = %+v, want 24h from default", c.AgentTTL)
	}
	if c.ConfirmTimeout != (Setting[time.Duration]{60 * time.Second, Default}) {
		t.Errorf("confirm_timeout = %+v, want 60s from default", c.ConfirmTimeout)
	}
}

func TestBaseAndMaxEditorsHaveNoDefault(t *testing.T) {
	c := load(t, Inputs{Home: tempDir(t), Repo: tempDir(t)})
	if c.Base != (Setting[string]{"", Unset}) || c.Base.Set() {
		t.Errorf("base = %+v, set %v, want unset", c.Base, c.Base.Set())
	}
	if c.MaxEditors != (Setting[int]{0, Unset}) || c.MaxEditors.Set() {
		t.Errorf("max_editors = %+v, set %v, want unset", c.MaxEditors, c.MaxEditors.Set())
	}
}

func TestEditorRAMBudgetDefaultsToAQuarterOfPhysicalRAM(t *testing.T) {
	for _, tt := range []struct {
		ram  uint64
		want float64
	}{
		{64 << 30, 16},
		{36 << 30, 9},
		{18 << 30, 4.5},
	} {
		memory := func() (uint64, error) { return tt.ram, nil }
		c := load(t, Inputs{Home: tempDir(t), Memory: memory})
		if c.EditorRAMBudgetGB != (Setting[float64]{tt.want, Default}) {
			t.Errorf("RAM %d bytes: editor_ram_budget_gb = %+v, want %v from default",
				tt.ram, c.EditorRAMBudgetGB, tt.want)
		}
	}
}

func TestPhysicalRAMIsReadOnlyWhenTheBudgetIsUnset(t *testing.T) {
	broken := errors.New("no sysctl here")
	memory := func() (uint64, error) { return 0, broken }

	_, err := Load(Inputs{Home: tempDir(t), Memory: memory})
	if !errors.Is(err, broken) || !strings.Contains(err.Error(), "editor_ram_budget_gb") {
		t.Errorf("budget unset, Memory failing: Load error = %v, want one naming "+
			"editor_ram_budget_gb and wrapping Memory's", err)
	}

	home := tempDir(t)
	writeUser(t, home, "editor_ram_budget_gb = 12\n")
	c, err := Load(Inputs{Home: home, Memory: memory})
	if err != nil {
		t.Fatalf("budget set, Memory failing: Load error = %v, want nil", err)
	}
	if c.EditorRAMBudgetGB != (Setting[float64]{12, User}) {
		t.Errorf("editor_ram_budget_gb = %+v, want 12 from the user file", c.EditorRAMBudgetGB)
	}
}

func TestPrecedenceFlagEnvRepoUserDefault(t *testing.T) {
	// ready, max_slots and base are the keys every layer takes. Each case sets them in its own
	// layer and every layer below it, so the layer it names must win over all of those.
	type layer struct {
		ready, maxSlots int
		base            string
	}
	flag := layer{11, 21, "from-flag"}
	envL := layer{12, 22, "from-env"}
	repoL := layer{13, 23, "from-repo"}
	userL := layer{14, 24, "from-user"}
	for _, tt := range []struct {
		top     Source
		want    layer
		baseSrc Source // base has no default, so it is unset where the others are default
	}{
		{Flag, flag, Flag},
		{Env, envL, Env},
		{Repo, repoL, Repo},
		{User, userL, User},
		{Default, layer{1, 3, ""}, Unset},
	} {
		t.Run(string(tt.top), func(t *testing.T) {
			home, repo := tempDir(t), tempDir(t)
			in := Inputs{Home: home, Repo: repo}
			below := false
			for _, l := range []struct {
				src Source
				set func()
			}{
				{Flag, func() {
					in.Flags = map[string]string{
						"ready": "11", "max_slots": "21", "base": flag.base,
					}
				}},
				{Env, func() {
					in.Getenv = env(map[string]string{
						"UPOOL_READY": "12", "UPOOL_MAX_SLOTS": "22", "UPOOL_BASE": envL.base,
					})
				}},
				{Repo, func() {
					writeRepo(t, repo, "ready = 13\nmax_slots = 23\nbase = \"from-repo\"\n")
				}},
				{User, func() {
					writeUser(t, home, "ready = 14\nmax_slots = 24\nbase = \"from-user\"\n")
				}},
			} {
				below = below || l.src == tt.top
				if below {
					l.set()
				}
			}
			c := load(t, in)
			if c.Ready != (Setting[int]{tt.want.ready, tt.top}) {
				t.Errorf("ready = %+v, want %d from %s", c.Ready, tt.want.ready, tt.top)
			}
			if c.MaxSlots != (Setting[int]{tt.want.maxSlots, tt.top}) {
				t.Errorf("max_slots = %+v, want %d from %s", c.MaxSlots, tt.want.maxSlots, tt.top)
			}
			if c.Base != (Setting[string]{tt.want.base, tt.baseSrc}) {
				t.Errorf("base = %+v, want %q from %s", c.Base, tt.want.base, tt.baseSrc)
			}
		})
	}
}

func TestEachSettingTakesItsOwnHighestLayer(t *testing.T) {
	home, repo := tempDir(t), tempDir(t)
	writeUser(t, home, "ready = 4\nmax_slots = 4\nbase = \"u\"\neditor_ram_gb = 3\n")
	writeRepo(t, repo, "max_slots = 5\nbase = \"r\"\n")
	c := load(t, Inputs{
		Home:   home,
		Repo:   repo,
		Getenv: env(map[string]string{"UPOOL_BASE": "e"}),
		Flags:  map[string]string{"agent_ttl": "2h"},
	})
	want := map[string]Source{
		"ready": User, "max_slots": Repo, "base": Env, "editor_ram_gb": User, "agent_ttl": Flag,
		"max_cold_imports": Default, "editor_ram_budget_gb": Default, "helper_ram_gb": Default,
		"shrink_after": Default, "confirm_timeout": Default, "max_editors": Unset,
	}
	if got := sources(c); !maps.Equal(got, want) {
		t.Errorf("sources = %v, want %v", got, want)
	}
	if c.Ready.Value != 4 || c.MaxSlots.Value != 5 || c.Base.Value != "e" ||
		c.EditorRAMGB.Value != 3 || c.AgentTTL.Value != 2*time.Hour {
		t.Errorf("ready, max_slots, base, editor_ram_gb, agent_ttl = %d, %d, %q, %v, %v, "+
			"want 4, 5, \"e\", 3, 2h", c.Ready.Value, c.MaxSlots.Value, c.Base.Value,
			c.EditorRAMGB.Value, c.AgentTTL.Value)
	}
}

func TestEnvNameIsUpoolAndTheKeyInCapitals(t *testing.T) {
	if got := EnvName("max_slots"); got != "UPOOL_MAX_SLOTS" {
		t.Errorf("EnvName(max_slots) = %q, want UPOOL_MAX_SLOTS", got)
	}
	vars := map[string]string{}
	for k, v := range nonDefault {
		vars["UPOOL_"+strings.ToUpper(k)] = v
	}
	c := load(t, Inputs{Home: tempDir(t), Getenv: env(vars)})
	for k, src := range sources(c) {
		if src != Env {
			t.Errorf("%s source = %s, want env", k, src)
		}
	}
	checkNonDefault(t, c)
}

func TestFlagsSetEveryKey(t *testing.T) {
	c := load(t, Inputs{Home: tempDir(t), Flags: nonDefault})
	for k, src := range sources(c) {
		if src != Flag {
			t.Errorf("%s source = %s, want flag", k, src)
		}
	}
	checkNonDefault(t, c)
}

func TestUserFileTakesEveryKey(t *testing.T) {
	home := tempDir(t)
	writeUser(t, home, `ready = 2
max_slots = 5
base = "develop"
max_cold_imports = 2
editor_ram_budget_gb = 20
editor_ram_gb = 3
helper_ram_gb = 1.5
max_editors = 4
shrink_after = "7d"
agent_ttl = "12h"
confirm_timeout = "90s"
`)
	c := load(t, Inputs{Home: home})
	for k, src := range sources(c) {
		if src != User {
			t.Errorf("%s source = %s, want user", k, src)
		}
	}
	checkNonDefault(t, c)
}

func TestEmptyEnvironmentVariableIsUnset(t *testing.T) {
	c := load(t, Inputs{Home: tempDir(t), Getenv: env(map[string]string{"UPOOL_READY": ""})})
	if c.Ready != (Setting[int]{1, Default}) {
		t.Errorf("ready = %+v, want 1 from default", c.Ready)
	}
}

func TestNoRepoReadsOnlyTheUserFile(t *testing.T) {
	home := tempDir(t)
	writeUser(t, home, "max_slots = 4\n")
	c := load(t, Inputs{Home: home})
	if c.MaxSlots != (Setting[int]{4, User}) {
		t.Errorf("max_slots = %+v, want 4 from the user file", c.MaxSlots)
	}
}

func TestUnknownKeyNamesFileAndKey(t *testing.T) {
	for _, tt := range []struct {
		name, doc, key string
	}{
		{"misspelt", "ready = 1\nmax_slot = 4\n", "max_slot"},
		{"table", "[lanes]\nscript = [\"Assets/Scripts\"]\n", "lanes"},
		{"dotted", "editor.ram_gb = 3\n", "editor"},
	} {
		t.Run("user/"+tt.name, func(t *testing.T) {
			home := tempDir(t)
			writeUser(t, home, tt.doc)
			err := loadErr(t, Inputs{Home: home})
			want := `~/.config/upool/config.toml: unknown key "` + tt.key + `"`
			if err.Error() != want {
				t.Errorf("Load error = %q, want %q", err, want)
			}
		})
		t.Run("repo/"+tt.name, func(t *testing.T) {
			repo := tempDir(t)
			writeRepo(t, repo, tt.doc)
			err := loadErr(t, Inputs{Home: tempDir(t), Repo: repo})
			want := RepoPath(repo) + `: unknown key "` + tt.key + `"`
			if err.Error() != want {
				t.Errorf("Load error = %q, want %q", err, want)
			}
		})
	}
}

func TestRepoFileRejectsMachineKeys(t *testing.T) {
	user := map[string]string{
		"max_cold_imports":     "2",
		"editor_ram_budget_gb": "20",
		"editor_ram_gb":        "3",
		"helper_ram_gb":        "1.5",
		"max_editors":          "4",
		"shrink_after":         `"7d"`,
		"agent_ttl":            `"12h"`,
		"confirm_timeout":      `"90s"`,
	}
	if got, want := len(user)+len(repoKeys), len(keys); got != want {
		t.Fatalf("the test covers %d keys, want all %d", got, want)
	}
	for key, value := range user {
		t.Run(key, func(t *testing.T) {
			home, repo := tempDir(t), tempDir(t)
			doc := "ready = 1\n" + key + " = " + value + "\n"
			writeRepo(t, repo, doc)
			err := loadErr(t, Inputs{Home: home, Repo: repo})
			msg := err.Error()
			if !strings.HasPrefix(msg, RepoPath(repo)+": ") || !strings.Contains(msg, `"`+key+`"`) {
				t.Errorf("Load error = %q, want one naming %s and %q", msg, RepoPath(repo), key)
			}

			// The same document is a valid user file.
			writeUser(t, home, doc)
			if err := os.Remove(RepoPath(repo)); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(Inputs{Home: home, Repo: repo, Memory: ram64}); err != nil {
				t.Errorf("as the user file: Load error = %v, want nil", err)
			}
		})
	}
}

func TestBadValueNamesWhereItCameFrom(t *testing.T) {
	for _, tt := range []struct {
		name  string
		user  string
		env   map[string]string
		flags map[string]string
		want  []string // substrings of the error
	}{
		{name: "user file, wrong type", user: "ready = \"one\"\n",
			want: []string{userShown, `"ready"`}},
		{name: "user file, fractional count", user: "max_slots = 2.5\n",
			want: []string{userShown, `"max_slots"`}},
		{name: "user file, bad duration", user: "confirm_timeout = \"1 minute\"\n",
			want: []string{userShown, `"confirm_timeout"`, `"1 minute" is not a duration`}},
		{name: "user file, duration as a number", user: "agent_ttl = 24\n",
			want: []string{userShown, `"agent_ttl"`, `"24" is not a duration`}},
		{name: "user file, not TOML", user: "ready = = 1\n", want: []string{userShown}},
		{name: "env, not an integer", env: map[string]string{"UPOOL_READY": "many"},
			want: []string{"UPOOL_READY", `"many" is not an integer`}},
		{name: "env, not a number", env: map[string]string{"UPOOL_HELPER_RAM_GB": "2GB"},
			want: []string{"UPOOL_HELPER_RAM_GB", `"2GB" is not a number`}},
		{name: "env, bad duration", env: map[string]string{"UPOOL_SHRINK_AFTER": "2w"},
			want: []string{"UPOOL_SHRINK_AFTER", `"2w" is not a duration`}},
		{name: "flag, not an integer", flags: map[string]string{"max_slots": "x"},
			want: []string{"flag for max_slots", `"x" is not an integer`}},
		{name: "flag, unknown key", flags: map[string]string{"slots": "2"},
			want: []string{`flag for unknown key "slots"`}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			home := tempDir(t)
			if tt.user != "" {
				writeUser(t, home, tt.user)
			}
			err := loadErr(t, Inputs{Home: home, Getenv: env(tt.env), Flags: tt.flags})
			for _, w := range tt.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("Load error = %q, want one containing %q", err, w)
				}
			}
		})
	}
}

func TestRelativeHomeOrRepoIsRefused(t *testing.T) {
	for _, in := range []Inputs{{Home: ""}, {Home: "home"}, {Home: tempDir(t), Repo: "repo"}} {
		if _, err := Load(in); err == nil {
			t.Errorf("Load(Home %q, Repo %q) error = nil, want an error", in.Home, in.Repo)
		}
	}
}

func TestUnreadableFileIsAnError(t *testing.T) {
	home := tempDir(t)
	// A directory where the user file should be cannot be read as one.
	if err := os.MkdirAll(UserPath(home), 0o755); err != nil {
		t.Fatal(err)
	}
	err := loadErr(t, Inputs{Home: home})
	if !strings.Contains(err.Error(), "reading "+userShown) {
		t.Errorf("Load error = %q, want one about reading %s", err, userShown)
	}
}

func TestParseDuration(t *testing.T) {
	for _, tt := range []struct {
		in   string
		want time.Duration
	}{
		{"0s", 0},
		{"60s", 60 * time.Second},
		{"5m", 5 * time.Minute},
		{"24h", 24 * time.Hour},
		{"14d", 14 * 24 * time.Hour},
		{"007m", 7 * time.Minute},
	} {
		got, err := parseDuration(tt.in)
		if err != nil || got != tt.want {
			t.Errorf("parseDuration(%q) = %v, %v, want %v, nil", tt.in, got, err, tt.want)
		}
	}
	for _, in := range []string{
		"", "s", "24", "24 h", " 24h", "24h ", "24H", "1w", "1h30m", "1.5h", "-1h", "+1h", "1_000s",
		"106752d", "99999999999999999999s",
	} {
		if got, err := parseDuration(in); err == nil {
			t.Errorf("parseDuration(%q) = %v, nil, want an error", in, got)
		}
	}
}

// What init writes is read back as the repo layer, with a base that needs TOML quoting intact.
func TestWriteRepoFileRoundTripsThroughLoad(t *testing.T) {
	home, repo := tempDir(t), tempDir(t)
	want := RepoFile{Ready: 2, MaxSlots: 4, Base: `release/"2026"`}
	written, err := WriteRepoFile(repo, want)
	if err != nil || !written {
		t.Fatalf("WriteRepoFile = %v, %v, want true, nil", written, err)
	}
	c := load(t, Inputs{Repo: repo, Home: home})
	got := RepoFile{Ready: c.Ready.Value, MaxSlots: c.MaxSlots.Value, Base: c.Base.Value}
	if got != want {
		t.Errorf("Load after WriteRepoFile = %+v, want %+v", got, want)
	}
	for key, src := range map[string]Source{
		"ready": c.Ready.Source, "max_slots": c.MaxSlots.Source, "base": c.Base.Source,
	} {
		if src != Repo {
			t.Errorf("%s source = %s, want %s", key, src, Repo)
		}
	}
}

func TestWriteRepoFileWritesTheRepoKeysInOrder(t *testing.T) {
	repo := tempDir(t)
	if _, err := WriteRepoFile(repo, RepoFile{Ready: 1, MaxSlots: 3, Base: "main"}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(RepoPath(repo))
	if err != nil {
		t.Fatal(err)
	}
	want := repoFileHeader + "ready = 1\nmax_slots = 3\nbase = \"main\"\n"
	if string(got) != want {
		t.Errorf(".upool.toml = %q, want %q", got, want)
	}
	// RepoFile must keep up with the keys the repo file takes.
	var keys []string
	for _, line := range strings.Split(strings.TrimSpace(string(got)), "\n")[1:] {
		key, _, _ := strings.Cut(line, " = ")
		keys = append(keys, key)
	}
	if !slices.Equal(keys, repoKeys) {
		t.Errorf("keys written = %q, want the repo file's keys %q", keys, repoKeys)
	}
}

func TestWriteRepoFileLeavesAnExistingFileAlone(t *testing.T) {
	repo := tempDir(t)
	const existing = "# the team's\nready = 2\n"
	writeRepo(t, repo, existing)
	written, err := WriteRepoFile(repo, RepoFile{Ready: 1, MaxSlots: 3, Base: "main"})
	if err != nil || written {
		t.Errorf("WriteRepoFile over an existing file = %v, %v, want false, nil", written, err)
	}
	if got, err := os.ReadFile(RepoPath(repo)); err != nil || string(got) != existing {
		t.Errorf(".upool.toml = %q, %v, want it unchanged: %q", got, err, existing)
	}
}

func TestWriteRepoFileRefusesARelativeRepository(t *testing.T) {
	if written, err := WriteRepoFile("project", RepoFile{}); err == nil || written {
		t.Errorf("WriteRepoFile(project) = %v, %v, want false and an error", written, err)
	}
}

// What setup writes sets nothing: Load reads it back with every setting from its default, and it
// names every key, so that a user reading it learns what can be set.
func TestWriteUserFileNamesEveryKeyAndSetsNone(t *testing.T) {
	home := tempDir(t)
	written, err := WriteUserFile(home)
	if err != nil || !written {
		t.Fatalf("WriteUserFile = %v, %v, want true, nil", written, err)
	}
	c := load(t, Inputs{Home: home})
	if c.Ready.Source != Default || c.Base.Source != Unset || c.MaxEditors.Source != Unset {
		t.Errorf("sources after WriteUserFile = ready %s, base %s, max_editors %s, want default, "+
			"unset, unset", c.Ready.Source, c.Base.Source, c.MaxEditors.Source)
	}
	got, err := os.ReadFile(UserPath(home))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSuffix(string(got), "\n"), "\n") {
		if line != "#" && !strings.HasPrefix(line, "# ") {
			t.Errorf("config.toml line %q is not a comment", line)
		}
	}
	for _, key := range keys {
		if !strings.Contains(string(got), "#   "+key+" ") {
			t.Errorf("config.toml does not name %s", key)
		}
	}
}

func TestWriteUserFileLeavesAnExistingFileAlone(t *testing.T) {
	home := tempDir(t)
	const existing = "max_slots = 5\n"
	writeUser(t, home, existing)
	written, err := WriteUserFile(home)
	if err != nil || written {
		t.Errorf("WriteUserFile over an existing file = %v, %v, want false, nil", written, err)
	}
	if got, err := os.ReadFile(UserPath(home)); err != nil || string(got) != existing {
		t.Errorf("config.toml = %q, %v, want it unchanged: %q", got, err, existing)
	}
}

func TestWriteUserFileRefusesARelativeHome(t *testing.T) {
	if written, err := WriteUserFile("home"); err == nil || written {
		t.Errorf("WriteUserFile(home) = %v, %v, want false and an error", written, err)
	}
}
