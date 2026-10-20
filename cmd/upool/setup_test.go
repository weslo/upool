package main

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/weslo/upool/assets"
	"github.com/weslo/upool/internal/block"
	"github.com/weslo/upool/internal/config"
	"github.com/weslo/upool/internal/output"
)

// setupEnv is the environment setup's tests run in: a temporary HOME, and PATH holding upool as
// Homebrew leaves it, a link in a bin directory to the binary elsewhere, which here is this test
// binary standing in for the Cellar's.
type setupEnv struct {
	env  map[string]string
	home string
	// upool is the link on PATH, and exe the test binary it points at, as os.Executable gives it.
	upool, exe string
}

// newSetupEnv makes a setup environment.
func newSetupEnv(t *testing.T) setupEnv {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin := tempDir(t)
	link := filepath.Join(bin, "upool")
	if err := os.Symlink(exe, link); err != nil {
		t.Fatal(err)
	}
	env := testEnv(t)
	env["PATH"] = bin
	return setupEnv{env: env, home: env["HOME"], upool: link, exe: exe}
}

// setup runs upool setup, with args, in e.
func (e setupEnv) setup(t *testing.T, args ...string) result {
	t.Helper()
	return dispatch(t, commandTable, hookTable, e.env, "", append([]string{"setup"}, args...)...)
}

// hooks is ~/.upool/hooks in e.
func (e setupEnv) hooks() string {
	return filepath.Join(e.home, ".upool", "hooks")
}

// script is the hook script for event in e.
func (e setupEnv) script(event string) string {
	return filepath.Join(e.hooks(), event+".sh")
}

// skills are the copies of the skill setup installs in e, in skillFiles' order.
func (e setupEnv) skills() []string {
	return homePaths(e, skillFiles)
}

// instructions are the instruction files setup upserts upool's block into in e, in
// instructionFiles' order.
func (e setupEnv) instructions() []string {
	return homePaths(e, instructionFiles)
}

// homePaths returns names, slash-separated paths relative to e's home, as absolute paths.
func homePaths(e setupEnv, names []string) []string {
	var files []string
	for _, name := range names {
		files = append(files, filepath.Join(e.home, filepath.FromSlash(name)))
	}
	return files
}

// events are the events `upool hook` handles, sorted, as setup lists their scripts.
func events() []string {
	return slices.Sorted(maps.Keys(hookTable))
}

// setupData decodes setup's --json data from r, failing the test unless it succeeded.
func setupData(t *testing.T, r result) setupReport {
	t.Helper()
	if r.code != output.ExitOK {
		t.Fatalf("exit = %d, want %d; stderr %q", r.code, output.ExitOK, r.stderr)
	}
	env := decode(t, r.stdout)
	if !env.Success || env.Command != "setup" {
		t.Fatalf("envelope = %+v, want setup's, succeeding", env)
	}
	data, _ := json.Marshal(env.Data) // it was decoded from JSON, so it encodes
	var report setupReport
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatal(err)
	}
	return report
}

// The acceptance test of #66: three executable scripts, each clean under sh -n and ending in an
// exec of the path upool was found through on PATH, and a second run changes nothing.
func TestSetupInstallsHookScripts(t *testing.T) {
	e := newSetupEnv(t)
	r := e.setup(t)
	if r.code != output.ExitOK {
		t.Fatalf("exit = %d, want %d; stderr %q", r.code, output.ExitOK, r.stderr)
	}
	want := []string{"created " + e.hooks()}
	for _, event := range events() {
		want = append(want, "created "+e.script(event))
	}
	want = append(want, "the hook scripts run "+e.upool, "created "+settingsPath(e.home))
	for _, file := range e.skills() {
		want = append(want, "created "+file)
	}
	for _, file := range e.instructions() {
		want = append(want, "created "+file+" with upool's block")
	}
	want = append(want, "created "+config.UserPath(e.home))
	if got := strings.Join(want, "\n") + "\n"; r.stdout != got {
		t.Errorf("stdout = %q, want %q", r.stdout, got)
	}

	entries, err := os.ReadDir(e.hooks())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	var wantNames []string
	for _, event := range events() {
		wantNames = append(wantNames, event+".sh")
	}
	if !slices.Equal(names, wantNames) {
		t.Errorf("~/.upool/hooks holds %q, want %q", names, wantNames)
	}
	checkMode(t, e.hooks(), hooksMode)
	before := map[string]os.FileInfo{}
	for _, event := range events() {
		file := e.script(event)
		checkMode(t, file, scriptMode)
		if out, err := exec.Command("sh", "-n", file).CombinedOutput(); err != nil {
			t.Errorf("sh -n %s: %v: %s", file, err, out)
		}
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
		if got, want := lines[len(lines)-1], "exec '"+e.upool+"' hook "+event; got != want {
			t.Errorf("%s ends in %q, want %q", event, got, want)
		}
		if strings.Contains(string(b), e.exe) {
			t.Errorf("%s names the binary %s that the link on PATH resolves to", event, e.exe)
		}
		info, err := os.Stat(file)
		if err != nil {
			t.Fatal(err)
		}
		before[file] = info
	}
	info, err := os.Stat(settingsPath(e.home))
	if err != nil {
		t.Fatal(err)
	}
	before[settingsPath(e.home)] = info
	for _, file := range append(e.skills(), e.instructions()...) {
		info, err := os.Stat(file)
		if err != nil {
			t.Fatal(err)
		}
		before[file] = info
	}
	userFile, err := os.ReadFile(config.UserPath(e.home))
	if err != nil {
		t.Fatal(err)
	}

	r = e.setup(t)
	if r.code != output.ExitOK {
		t.Fatalf("second run: exit = %d, want %d; stderr %q", r.code, output.ExitOK, r.stderr)
	}
	want = []string{e.hooks() + " is up to date"}
	for _, event := range events() {
		want = append(want, e.script(event)+" is up to date")
	}
	want = append(want, "the hook scripts run "+e.upool, settingsPath(e.home)+" is up to date")
	for _, file := range e.skills() {
		want = append(want, file+" is up to date")
	}
	for _, file := range e.instructions() {
		want = append(want, file+" already has upool's block")
	}
	want = append(want, "kept the existing "+config.UserPath(e.home))
	if got := strings.Join(want, "\n") + "\n"; r.stdout != got {
		t.Errorf("second run: stdout = %q, want %q", r.stdout, got)
	}
	for file, old := range before {
		info, err := os.Stat(file)
		if err != nil {
			t.Fatal(err)
		}
		if !os.SameFile(info, old) || !info.ModTime().Equal(old.ModTime()) {
			t.Errorf("second run rewrote %s", file)
		}
	}
	if got, err := os.ReadFile(config.UserPath(e.home)); err != nil ||
		string(got) != string(userFile) {
		t.Errorf("second run: config.toml = %q, %v, want it unchanged", got, err)
	}
}

// checkMode fails the test unless path's permission bits are want.
func checkMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Errorf("%s mode = %v, want %v", path, got, want)
	}
}

// A later run puts back what changed, a script's text or mode and the directory's mode, says so,
// leaves the rest alone, and never rewrites a config.toml that exists.
func TestSetupRewritesOnlyWhatChanged(t *testing.T) {
	e := newSetupEnv(t)
	setupData(t, e.setup(t, "--json"))
	if err := os.Chmod(e.hooks(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.script("worktree-remove"), []byte("#!/bin/sh\nexec '/old/upool' "+
		"hook worktree-remove\n"), scriptMode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(e.script("session-start"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.skills()[1], []byte("an older skill\n"), skillMode); err != nil {
		t.Fatal(err)
	}
	const userFile = "max_slots = 5\n"
	if err := os.WriteFile(config.UserPath(e.home), []byte(userFile), 0o644); err != nil {
		t.Fatal(err)
	}

	got := setupData(t, e.setup(t, "--json"))
	want := setupReport{
		Upool: e.upool,
		Hooks: installed{e.hooks(), updated},
		Scripts: []installed{
			{e.script("session-start"), updated},
			{e.script("worktree-create"), unchanged},
			{e.script("worktree-remove"), updated},
		},
		Settings: installed{settingsPath(e.home), unchanged},
		Skills:   []installed{{e.skills()[0], unchanged}, {e.skills()[1], updated}},
		Blocks: []upserted{
			{e.instructions()[0], block.Unchanged},
			{e.instructions()[1], block.Unchanged},
		},
		Config: installed{config.UserPath(e.home), unchanged},
	}
	if string(mustJSON(t, got)) != string(mustJSON(t, want)) {
		t.Errorf("data = %s, want %s", mustJSON(t, got), mustJSON(t, want))
	}
	checkMode(t, e.hooks(), hooksMode)
	checkMode(t, e.script("session-start"), scriptMode)
	if b, err := os.ReadFile(e.script("worktree-remove")); err != nil ||
		!strings.HasSuffix(string(b), "exec '"+e.upool+"' hook worktree-remove\n") {
		t.Errorf("worktree-remove.sh = %q, %v, want it to exec %s", b, err, e.upool)
	}
	if b, err := os.ReadFile(e.skills()[1]); err != nil || string(b) != assets.Skill {
		t.Errorf("%s = %q, %v, want the skill", e.skills()[1], b, err)
	}
	if b, err := os.ReadFile(config.UserPath(e.home)); err != nil || string(b) != userFile {
		t.Errorf("config.toml = %q, %v, want it unchanged: %q", b, err, userFile)
	}
}

// The scripts run the first upool on PATH that is this binary, passing over another upool before
// it, and a path that needs quoting is quoted for sh.
func TestSetupRunsTheUpoolOnPathThatIsThisBinary(t *testing.T) {
	e := newSetupEnv(t)
	other := tempDir(t)
	if err := os.WriteFile(filepath.Join(other, "upool"), []byte("#!/bin/sh\n"),
		0o755); err != nil {
		t.Fatal(err)
	}
	quoted := filepath.Join(tempDir(t), "it's here")
	if err := os.Mkdir(quoted, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(quoted, "upool")
	if err := os.Symlink(e.exe, link); err != nil {
		t.Fatal(err)
	}
	e.env["PATH"] = strings.Join([]string{"bin", other, quoted, filepath.Dir(e.upool)},
		string(filepath.ListSeparator))

	got := setupData(t, e.setup(t, "--json"))
	if got.Upool != link {
		t.Errorf("upool = %q, want %q", got.Upool, link)
	}
	file := e.script("worktree-create")
	out, err := exec.Command("sh", "-c", `sed '$s/^exec /printf "%s\\n" /' "$1" | sh`, "sh",
		file).CombinedOutput()
	if err != nil {
		t.Fatalf("running %s with exec replaced by printf: %v: %s", file, err, out)
	}
	if want := link + "\nhook\nworktree-create\n"; string(out) != want {
		t.Errorf("%s's exec arguments = %q, want %q", file, out, want)
	}
}

// Without this binary on PATH there is no path for the scripts to run that survives an upgrade,
// so setup refuses and writes nothing.
func TestSetupRefusesWhenThisBinaryIsNotOnPath(t *testing.T) {
	for name, path := range map[string]func(t *testing.T, e setupEnv) string{
		"no upool": func(t *testing.T, _ setupEnv) string { return tempDir(t) },
		"another upool": func(t *testing.T, _ setupEnv) string {
			return filepath.Dir(writeOtherUpool(t))
		},
		// The entry reaches this binary from the test's working directory, but the helper
		// does not run the scripts from setup's.
		"only relative": func(t *testing.T, e setupEnv) string {
			wd, err := os.Getwd()
			if err != nil {
				t.Fatal(err)
			}
			rel, err := filepath.Rel(wd, filepath.Dir(e.upool))
			if err != nil {
				t.Fatal(err)
			}
			return rel
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := newSetupEnv(t)
			e.env["PATH"] = path(t, e)
			r := e.setup(t)
			checkSetupRefused(t, e, r, "no upool on PATH is this binary, "+e.exe)
		})
	}
}

// writeOtherUpool writes an executable upool that is not this binary, and returns its path.
func writeOtherUpool(t *testing.T) string {
	t.Helper()
	file := filepath.Join(tempDir(t), "upool")
	if err := os.WriteFile(file, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return file
}

// A home directory in a folder the desktop app's helper cannot read from, by name in any case or
// through a link, is refused before anything is written (#66).
func TestSetupRefusesAHomeInAProtectedFolder(t *testing.T) {
	for _, home := range []string{"Documents/home", "Desktop/home", "Downloads/home",
		"documents/home", "Documents", "link"} {
		t.Run(home, func(t *testing.T) {
			e := newSetupEnv(t)
			root := tempDir(t)
			dir := filepath.Join(root, filepath.FromSlash(home))
			if home == "link" {
				target := filepath.Join(root, "Documents", "home")
				if err := os.MkdirAll(target, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, dir); err != nil {
					t.Fatal(err)
				}
			} else if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			e.env["HOME"], e.home = dir, dir
			r := e.setup(t)
			checkSetupRefused(t, e, r, "which the desktop app's hook helper cannot read from")
		})
	}
}

// checkSetupRefused fails the test unless r is a refusal whose message contains want, and setup
// wrote nothing under e's home, ~/.claude included.
func checkSetupRefused(t *testing.T, e setupEnv, r result, want string) {
	t.Helper()
	if r.code != output.ExitFailure {
		t.Errorf("exit = %d, want %d", r.code, output.ExitFailure)
	}
	if !strings.Contains(r.stderr, want) {
		t.Errorf("stderr = %q, want it to contain %q", r.stderr, want)
	}
	for _, path := range []string{filepath.Join(e.home, ".upool"), config.UserPath(e.home),
		filepath.Join(e.home, ".claude")} {
		if _, err := os.Lstat(path); err == nil {
			t.Errorf("%s exists after a refusal", path)
		}
	}
}

// The acceptance test of #68: both copies of the skill, and one block in each instruction file
// after two runs, with the other content untouched.
func TestSetupInstallsSkillAndBlock(t *testing.T) {
	e := newSetupEnv(t)
	originals := []string{"# My rules\n\nAlways run the tests.\n", "Use tabs."}
	for i, file := range e.instructions() {
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(originals[i]), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	other := filepath.Join(e.home, ".claude", "skills", "other", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(other), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, []byte("another skill\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	first := setupData(t, e.setup(t, "--json"))
	second := setupData(t, e.setup(t, "--json"))
	for i, file := range e.instructions() {
		if first.Blocks[i] != (upserted{file, block.Inserted}) {
			t.Errorf("first run: blocks[%d] = %+v, want %s inserted", i, first.Blocks[i], file)
		}
		if second.Blocks[i] != (upserted{file, block.Unchanged}) {
			t.Errorf("second run: blocks[%d] = %+v, want %s unchanged", i, second.Blocks[i], file)
		}
	}
	for i, file := range e.skills() {
		if second.Skills[i] != (installed{file, unchanged}) {
			t.Errorf("second run: skills[%d] = %+v, want %s unchanged", i, second.Skills[i], file)
		}
		b, err := os.ReadFile(file)
		if err != nil || string(b) != assets.Skill {
			t.Errorf("%s = %q, %v, want the skill", file, b, err)
		}
		checkMode(t, file, skillMode)
	}

	rendered := string(block.Block{Version: assets.ClaudeMDBlockVersion,
		Body: assets.ClaudeMDBlock}.Render())
	for i, file := range e.instructions() {
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		got := string(b)
		for _, marker := range []string{block.Begin(assets.ClaudeMDBlockVersion),
			block.End(assets.ClaudeMDBlockVersion)} {
			if n := strings.Count(got, marker); n != 1 {
				t.Errorf("%s has %q %d times, want 1", file, marker, n)
			}
		}
		sep := "\n"
		if !strings.HasSuffix(originals[i], "\n") {
			sep = "\n\n"
		}
		if want := originals[i] + sep + rendered; got != want {
			t.Errorf("%s = %q, want its own text, a blank line and the block: %q", file, got, want)
		}
	}
	if b, err := os.ReadFile(other); err != nil || string(b) != "another skill\n" {
		t.Errorf("%s = %q, %v, want it unchanged", other, b, err)
	}
}

// A newer upool's block is kept and reported, not replaced by this one's.
func TestSetupKeepsANewerBlock(t *testing.T) {
	e := newSetupEnv(t)
	file := e.instructions()[0]
	newer := string(block.Block{Version: assets.ClaudeMDBlockVersion + 1,
		Body: "a newer block\n"}.Render())
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(newer), 0o644); err != nil {
		t.Fatal(err)
	}
	r := e.setup(t)
	if r.code != output.ExitOK {
		t.Fatalf("exit = %d, want %d; stderr %q", r.code, output.ExitOK, r.stderr)
	}
	want := fmt.Sprintf("kept the newer upool block in %s: this upool writes v%d\n", file,
		assets.ClaudeMDBlockVersion)
	if !strings.Contains(r.stdout, want) {
		t.Errorf("stdout = %q, want a line %q", r.stdout, want)
	}
	if b, err := os.ReadFile(file); err != nil || string(b) != newer {
		t.Errorf("%s = %q, %v, want it unchanged", file, b, err)
	}
}

// An instruction file whose upool markers do not pair is refused before anything is written, and
// left as it was.
func TestSetupRefusesAnInstructionFileItCannotUpsert(t *testing.T) {
	e := newSetupEnv(t)
	file := e.instructions()[1]
	const doc = "Use tabs.\n<!-- upool:begin v1 -->\nno end marker\n"
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	r := e.setup(t)
	checkSetupRefused(t, e, r, file+": line 2 begins a upool block that no line after it ends")
	if b, err := os.ReadFile(file); err != nil || string(b) != doc {
		t.Errorf("%s = %q, %v, want it unchanged", file, b, err)
	}
	if _, err := os.Lstat(filepath.Join(e.home, ".agents")); err == nil {
		t.Errorf("~/.agents exists after a refusal")
	}
}
