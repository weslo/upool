// Package config resolves upool's settings. Each one comes from the first of five layers that sets
// it: a command-line flag, a UPOOL_* environment variable, the repository's committed .upool.toml,
// the user's ~/.config/upool/config.toml, then the default (docs/design.md, "One binary, a dozen
// commands, one JSON file per pool", the configuration paragraph). Unknown keys are errors, as in
// warmtree, so a misspelt key fails rather than silently leaving a default in place.
//
// The repo file carries only data, the keys repoKeys lists, never a machine's settings: it is
// committed and shared, and every other key in it is an error. The user file takes every key. Its
// path is fixed under the home directory passed in, never os.UserConfigDir, which on macOS is
// ~/Library/Application Support (docs/design.md, "Repository, CI and Homebrew distribution", the
// layout). Nothing here reads the environment or finds the home directory itself
// (CONTRIBUTING.md, "Process state comes in through run").
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Source is where a setting's value came from, for doctor and status.
type Source string

const (
	// Unset means nothing set the setting and it has no default: base until init writes it into
	// .upool.toml (A17), and max_editors, which internal/ledger then derives from the RAM settings
	// (A9).
	Unset Source = "unset"
	// Default means no layer set the setting, so it has its default.
	Default Source = "default"
	// User means the user file, UserPath, set it.
	User Source = "user"
	// Repo means the repository's .upool.toml, RepoPath, set it.
	Repo Source = "repo"
	// Env means the environment variable EnvName(key) set it.
	Env Source = "env"
	// Flag means the caller's command-line flag set it.
	Flag Source = "flag"
)

// Setting is one resolved setting: its value and where the value came from. Value is the zero
// value when Source is Unset.
type Setting[T any] struct {
	Value  T
	Source Source
}

// Set reports whether the setting has a value, from a layer or a default.
func (s Setting[T]) Set() bool {
	return s.Source != Unset
}

// Config is every setting, resolved. The defaults are those of docs/design.md's configuration
// paragraph and docs/contracts.md (h) RAM budget.
type Config struct {
	// Ready is how many ready slots the pool keeps; default 1.
	Ready Setting[int]
	// MaxSlots caps the pool's slots; default 3.
	MaxSlots Setting[int]
	// Base is the git ref slots are cloned at and reset to (docs/contracts.md (e) State schemas,
	// `base`). It has no default here: when nothing sets it, init resolves origin's default branch,
	// else the current branch, and writes that into .upool.toml (A17).
	Base Setting[string]
	// MaxColdImports caps the cold imports running at once; default 1.
	MaxColdImports Setting[int]
	// EditorRAMBudgetGB is the RAM, in GB, that Editors may be charged; default physical RAM / 4.
	EditorRAMBudgetGB Setting[float64]
	// EditorRAMGB is the charge, in GB, per long-lived Editor; default 2.5.
	EditorRAMGB Setting[float64]
	// HelperRAMGB is the charge, in GB, once any Editor is up; default 2.
	HelperRAMGB Setting[float64]
	// MaxEditors caps the Editors running at once. It is optional and unset unless a layer sets
	// it; unset, internal/ledger derives it from the budget (docs/contracts.md (h); A9).
	MaxEditors Setting[int]
	// ShrinkAfter is how long a slot beyond ready stays idle before prune may remove it;
	// default 14d.
	ShrinkAfter Setting[time.Duration]
	// MaxDisk is the disk, in GB, a pool's slots may use, by the private size status --du
	// reports (A32), past which prune removes idle ready slots, least recently used first. It is
	// optional, unset unless a layer sets it, and a machine's setting, which the repo file does
	// not take (docs/design.md, "Disk: 0.4 GB per persistent slot; disk is not what limits the
	// count"; #101).
	MaxDisk Setting[float64]
	// AgentTTL is how old a subagent worktree gets before prune may collect it; default 24h.
	AgentTTL Setting[time.Duration]
	// ConfirmTimeout is how long a create-hook lease waits for take --confirm; default 60s.
	ConfirmTimeout Setting[time.Duration]
}

// keys are every setting's key, in Config's order.
var keys = []string{
	"ready", "max_slots", "base", "max_cold_imports", "editor_ram_budget_gb", "editor_ram_gb",
	"helper_ram_gb", "max_editors", "shrink_after", "max_disk", "agent_ttl", "confirm_timeout",
}

// repoKeys are the keys the repo file accepts: the data docs/design.md lists for it. Its lane
// folder lists arrive with #31.
var repoKeys = []string{"ready", "max_slots", "base"}

// Inputs is everything Load reads from outside the package.
type Inputs struct {
	// Flags holds the values the caller's command-line flags set, by key (such as "base" for
	// take --base), as typed. A key that names no setting is an error.
	Flags map[string]string
	// Getenv looks up an environment variable, as run's getenv does, so a variable set to ""
	// counts as unset. Nil means an empty environment.
	Getenv func(string) string
	// Repo is the absolute path of the main checkout whose .upool.toml is read, or "" for none.
	Repo string
	// Home is the absolute path of the home directory, under which the user file is read.
	Home string
	// Memory returns the machine's physical RAM in bytes. Load calls it only when no layer sets
	// editor_ram_budget_gb. Nil means PhysicalMemory.
	Memory func() (uint64, error)
}

// userShown is how messages name the user file.
const userShown = "~/.config/upool/config.toml"

// UserPath returns the user file under home: home/.config/upool/config.toml.
func UserPath(home string) string {
	return filepath.Join(home, ".config", "upool", "config.toml")
}

// RepoPath returns the repo file of the main checkout repo: repo/.upool.toml.
func RepoPath(repo string) string {
	return filepath.Join(repo, ".upool.toml")
}

// EnvName returns the environment variable that sets key: UPOOL_ and the key in capitals, such as
// UPOOL_MAX_SLOTS.
func EnvName(key string) string {
	return "UPOOL_" + strings.ToUpper(key)
}

// gb is a gigabyte as the RAM settings count one, 2^30 bytes: 64 GB of RAM, which hw.memsize gives
// as 68719476736, is the budget of 16 in docs/contracts.md (h).
const gb = 1 << 30

// Load resolves every setting from in. A missing file sets nothing; a file that cannot be read or
// parsed, an unknown key, a key the repo file does not take and a value of the wrong type are
// errors that name the file or variable and the key.
func Load(in Inputs) (Config, error) {
	if !filepath.IsAbs(in.Home) {
		return Config{}, fmt.Errorf("home directory %q is not an absolute path", in.Home)
	}
	if in.Repo != "" && !filepath.IsAbs(in.Repo) {
		return Config{}, fmt.Errorf("repository %q is not an absolute path", in.Repo)
	}
	for _, k := range slices.Sorted(maps.Keys(in.Flags)) {
		if !slices.Contains(keys, k) {
			return Config{}, fmt.Errorf("flag for unknown key %q", k)
		}
	}
	user, err := readFile(UserPath(in.Home), userShown, false)
	if err != nil {
		return Config{}, err
	}
	var repo file
	if in.Repo != "" {
		path := RepoPath(in.Repo)
		if repo, err = readFile(path, path, true); err != nil {
			return Config{}, err
		}
	}
	r := resolver{flags: in.Flags, getenv: in.Getenv}
	if r.getenv == nil {
		r.getenv = func(string) string { return "" }
	}
	// Only the repo file's keys are passed its values; readFile has refused any other key there.
	var c Config
	c.Ready = or(lookup(&r, "ready", parseInt, repo.Ready, user.Ready), 1)
	c.MaxSlots = or(lookup(&r, "max_slots", parseInt, repo.MaxSlots, user.MaxSlots), 3)
	c.Base = lookup(&r, "base", parseString, repo.Base, user.Base)
	c.MaxColdImports = or(lookup(&r, "max_cold_imports", parseInt, nil, user.MaxColdImports), 1)
	c.EditorRAMBudgetGB = lookup(&r, "editor_ram_budget_gb", parseFloat, nil,
		user.EditorRAMBudgetGB)
	c.EditorRAMGB = or(lookup(&r, "editor_ram_gb", parseFloat, nil, user.EditorRAMGB), 2.5)
	c.HelperRAMGB = or(lookup(&r, "helper_ram_gb", parseFloat, nil, user.HelperRAMGB), 2)
	c.MaxEditors = lookup(&r, "max_editors", parseInt, nil, user.MaxEditors)
	c.ShrinkAfter = or(lookup(&r, "shrink_after", parseDuration, nil,
		(*time.Duration)(user.ShrinkAfter)), 14*day)
	c.MaxDisk = lookup(&r, "max_disk", parseFloat, nil, user.MaxDisk)
	c.AgentTTL = or(lookup(&r, "agent_ttl", parseDuration, nil,
		(*time.Duration)(user.AgentTTL)), 24*time.Hour)
	c.ConfirmTimeout = or(lookup(&r, "confirm_timeout", parseDuration, nil,
		(*time.Duration)(user.ConfirmTimeout)), 60*time.Second)
	if r.err != nil {
		return Config{}, r.err
	}
	if !c.EditorRAMBudgetGB.Set() {
		memory := in.Memory
		if memory == nil {
			memory = PhysicalMemory
		}
		ram, err := memory()
		if err != nil {
			return Config{}, fmt.Errorf("defaulting editor_ram_budget_gb to physical RAM / 4: %w",
				err)
		}
		c.EditorRAMBudgetGB = Setting[float64]{float64(ram) / gb / 4, Default}
	}
	return c, nil
}

// RepoFile is the repo file's keys, repoKeys, as `upool init` writes them: the values in effect
// when it runs, base included, so that they are visible, editable and shared (A17).
type RepoFile struct {
	Ready    int    `toml:"ready"`
	MaxSlots int    `toml:"max_slots"`
	Base     string `toml:"base"`
}

// repoFileHeader opens the repo file WriteRepoFile writes.
const repoFileHeader = "# upool's settings for this repository, written by upool init: " +
	"https://github.com/weslo/upool\n"

// WriteRepoFile writes f to the main checkout repo's .upool.toml, RepoPath(repo), unless that file
// already exists, and reports whether it wrote it. An existing file is left as it is, whatever it
// holds. A write that fails part-way removes the file, so the next run writes it whole.
func WriteRepoFile(repo string, f RepoFile) (written bool, err error) {
	if !filepath.IsAbs(repo) {
		return false, fmt.Errorf("repository %q is not an absolute path", repo)
	}
	var b strings.Builder
	b.WriteString(repoFileHeader)
	if err := toml.NewEncoder(&b).Encode(f); err != nil {
		return false, fmt.Errorf("encoding %s: %w", RepoPath(repo), err)
	}
	return writeNew(RepoPath(repo), b.String())
}

// writeNew writes data to a new file at path, with mode 0644, and reports whether it did: a file
// already at path is left as it is. A write that fails part-way removes the file, so the next run
// writes it whole.
func writeNew(path, data string) (written bool, err error) {
	// O_EXCL makes "unless it exists" and the create one step, so a file that appears meanwhile is
	// not overwritten either.
	out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, fs.ErrExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("writing %s: %w", path, err)
	}
	defer func() {
		if err != nil {
			// The write's error is the one worth reporting.
			_ = out.Close()
			_ = os.Remove(path)
		}
	}()
	if _, err := out.WriteString(data); err != nil {
		return false, fmt.Errorf("writing %s: %w", path, err)
	}
	if err := out.Close(); err != nil {
		return false, fmt.Errorf("closing %s: %w", path, err)
	}
	return true, nil
}

// file is either TOML file's contents. Every key is a pointer, so one the file leaves out is nil
// and falls through to the next layer.
type file struct {
	Ready             *int      `toml:"ready"`
	MaxSlots          *int      `toml:"max_slots"`
	Base              *string   `toml:"base"`
	MaxColdImports    *int      `toml:"max_cold_imports"`
	EditorRAMBudgetGB *float64  `toml:"editor_ram_budget_gb"`
	EditorRAMGB       *float64  `toml:"editor_ram_gb"`
	HelperRAMGB       *float64  `toml:"helper_ram_gb"`
	MaxEditors        *int      `toml:"max_editors"`
	ShrinkAfter       *duration `toml:"shrink_after"`
	MaxDisk           *float64  `toml:"max_disk"`
	AgentTTL          *duration `toml:"agent_ttl"`
	ConfirmTimeout    *duration `toml:"confirm_timeout"`
}

// readFile decodes the TOML file at path, which messages name as shown. The repo file, repo true,
// takes only repoKeys. A missing file sets nothing.
func readFile(path, shown string, repo bool) (file, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return file{}, nil
	}
	if err != nil {
		return file{}, fmt.Errorf("reading %s: %w", shown, err)
	}
	var f file
	md, err := toml.Decode(string(data), &f)
	if err != nil {
		return file{}, fmt.Errorf("reading %s: %w", shown, err)
	}
	// Undecoded lists, in the file's order, every key that matched no field of file. A table
	// lists its own key before those under it, and a dotted key starts with its top-level one.
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		return file{}, fmt.Errorf("%s: unknown key %q", shown, undecoded[0][0])
	}
	if repo {
		for _, k := range md.Keys() {
			if !slices.Contains(repoKeys, k[0]) {
				return file{}, fmt.Errorf("%s: key %q belongs in %s, not the repo file, which "+
					"takes only %s", shown, k[0], userShown, strings.Join(repoKeys, ", "))
			}
		}
	}
	return f, nil
}

// resolver looks settings up in the flag and environment layers, keeping the first error so that
// Load can resolve every setting in turn and check once.
type resolver struct {
	flags  map[string]string
	getenv func(string) string
	err    error
}

// lookup returns key's value from the first layer that sets it, parsing a flag or environment
// value with parse, or an Unset setting when none does. repo and user are the files' values, nil
// where a file leaves key out.
func lookup[T any](r *resolver, key string, parse func(string) (T, error),
	repo, user *T) Setting[T] {
	if r.err != nil {
		return Setting[T]{Source: Unset}
	}
	if s, ok := r.flags[key]; ok {
		v, err := parse(s)
		if err != nil {
			r.err = fmt.Errorf("flag for %s: %w", key, err)
		}
		return Setting[T]{v, Flag}
	}
	if s := r.getenv(EnvName(key)); s != "" {
		v, err := parse(s)
		if err != nil {
			r.err = fmt.Errorf("%s: %w", EnvName(key), err)
		}
		return Setting[T]{v, Env}
	}
	if repo != nil {
		return Setting[T]{*repo, Repo}
	}
	if user != nil {
		return Setting[T]{*user, User}
	}
	return Setting[T]{Source: Unset}
}

// or returns s, or def as a Default setting if s is Unset.
func or[T any](s Setting[T], def T) Setting[T] {
	if s.Set() {
		return s
	}
	return Setting[T]{def, Default}
}

func parseInt(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("%q is not an integer", s)
	}
	return n, nil
}

func parseFloat(s string) (float64, error) {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("%q is not a number", s)
	}
	return f, nil
}

func parseString(s string) (string, error) {
	return s, nil
}

// day is the d unit of a duration.
const day = 24 * time.Hour

// units are the units a duration may end in.
var units = map[byte]time.Duration{'s': time.Second, 'm': time.Minute, 'h': time.Hour, 'd': day}

// parseDuration reads a duration as the configuration writes one: a whole number and one unit, s,
// m, h or d, such as "14d". time.ParseDuration has no d, and takes forms such as "1h30m" and
// "1.5h" that this does not.
func parseDuration(s string) (time.Duration, error) {
	bad := fmt.Errorf("%q is not a duration: want a whole number and a unit, s, m, h or d, "+
		"such as \"24h\"", s)
	if len(s) < 2 {
		return 0, bad
	}
	unit, ok := units[s[len(s)-1]]
	digits := s[:len(s)-1]
	if !ok || strings.Trim(digits, "0123456789") != "" {
		return 0, bad
	}
	n, err := strconv.ParseInt(digits, 10, 64)
	if err != nil || n > math.MaxInt64/int64(unit) {
		return 0, fmt.Errorf("%q is too long a duration", s)
	}
	return time.Duration(n) * unit, nil
}

// duration is a duration in a TOML file, which must be a string parseDuration takes.
type duration time.Duration

// UnmarshalText parses text with parseDuration. The TOML decoder also passes it a non-string
// value as text, such as "24" for `agent_ttl = 24`, which has no unit and so is refused.
func (d *duration) UnmarshalText(text []byte) error {
	v, err := parseDuration(string(text))
	if err != nil {
		return err
	}
	*d = duration(v)
	return nil
}
