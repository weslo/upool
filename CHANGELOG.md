# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- `upool --version` prints the version. Any other invocation prints usage to stderr and exits 1;
  until now every invocation printed the version and exited 0.
- `internal/paths`: `Key`, the canonical form every path upool compares goes through (absolute,
  symlinks resolved on the longest existing prefix, cleaned, lowercased on a case-insensitive
  volume), and `Within` for comparing keys. Nothing calls it yet.
- `internal/proc`: `Start`, a process's start time to the microsecond from sysctl
  `kern.proc.pid` (an unsupported error off macOS), and `Alive`, which counts a process alive only
  while its PID and start time both match, so a reused PID reads as dead. The `Checker` interface
  lets later tests inject liveness. Nothing calls it yet.
- `internal/statefile`: `Lock`, the one `flock` on `~/.upool/lock` (creating `~/.upool` with mode
  0700), which waits for the holder and gives up when its context ends; `Update`, a read-modify-write
  of a state file under that lock that writes a temporary file, syncs it and renames it over the
  old one; and `Read`, which needs no lock because every write is a whole-file rename. Nothing calls
  it yet.
- `internal/registry`: the `~/.upool/registry.json` type, decoded strictly (an unknown key is an
  error, and a `schema_version` above 1 is refused with a message to upgrade upool); `Key`, the
  `<repo-key>` (the path's last element, `-`, and 8 hex digits of its SHA-256); `Lookup`, an exact
  match on the canonical path; and `Add`, which registers a path idempotently under the lock.
  Nothing calls it yet.
- `internal/pool` and `internal/ledger`: the `~/.upool/pools/<repo-key>/state.json` types (the
  pool, its slots with the seven lifecycle states as constants, leases, agent worktrees and the
  refresh job) and the `~/.upool/editors.json` type, each with `Load` and an `Update` that rewrites
  the file under the lock. Both decode strictly and refuse a `schema_version` above 1, as the
  registry does; nullable fields round-trip `null`, empty arrays are written as `[]`, and every
  timestamp is written in UTC. The `refresh_job` object's keys, which the contract had left
  unnamed, are `pid` and `pid_start`. Nothing calls them yet.
- `internal/pool`: `Next`, the slot lifecycle of docs/contracts.md (f) as a pure function from a
  slot and an event to the slot's next state: one of the seven states, or `removed` or `missing`.
  The events are the causes in (f), each carrying the facts `Next` cannot read from the slot
  (clean or dirty, holder alive, Editor live, a ready slot available); an event (f) gives no
  transition for is an error naming the state and the event. Nothing calls it yet.
- `internal/git`, the only way upool runs git: a `Runner` interface and its default, `Exec`, which
  runs `git -C <dir>` with `UNITY_SKIP_HOOKS=1` in an environment the caller passes, and refuses a
  relative directory rather than depend on the working directory. On top of it, `Status` parses
  `status --porcelain=v1 -z` (with untracked files listed whatever `status.showUntrackedFiles`
  says), `WorktreeList` parses `worktree list --porcelain -z` (path, HEAD, branch or detached,
  bare, locked and prunable with their reasons), and `HeadReachable` reports whether
  `for-each-ref --contains HEAD` prints a ref, the second half of a clean slot. Nothing calls it
  yet.
- `internal/config`: `Load` resolves each setting from a flag, its `UPOOL_*` variable
  (`UPOOL_MAX_SLOTS`), the repository's `.upool.toml`, `~/.config/upool/config.toml`, then its
  default, and records which one set it. The defaults are `ready` 1, `max_slots` 3,
  `max_cold_imports` 1, `editor_ram_budget_gb` physical RAM / 4, `editor_ram_gb` 2.5,
  `helper_ram_gb` 2, `shrink_after` 14d, `agent_ttl` 24h and `confirm_timeout` 60s; `base` and
  `max_editors` have none. Durations are a whole number and one unit, s, m, h or d. An unknown key
  in either file is an error naming the file and the key, and so is any key in `.upool.toml` but
  `ready`, `max_slots` and `base`. The TOML parser is `github.com/BurntSushi/toml`, upool's first
  dependency. Nothing calls it yet.
- `internal/output`: `Fail` and `NoTarget`, the errors commands return, which carry the exit code
  (`ExitFailure` or `ExitNoTarget`), an upool error code (`COMMAND_FAILED`, the Unity CLI's
  catch-all, or `NOT_IN_SLOT`; every code is a constant there) and a message, and keep both
  through `fmt.Errorf` wrapping. `Render` prints a command's result and returns its exit code: with
  `--json`, only the envelope on stdout, indented as the Unity CLI's are, with the error as its
  one `errors[]` entry; otherwise the data's human form on stdout and each warning and the error
  on stderr after `upool: `. Any other error renders as `COMMAND_FAILED` with exit 1. Nothing
  calls it yet.
- `upool hook worktree-create`, `upool hook worktree-remove` and `upool hook session-start`, what
  the hook scripts will run, print `not implemented` on stderr and nothing on stdout until M2.
  `worktree-create` exits 1; the other two exit 0, as Claude Code's hooks need
  (docs/contracts.md (d)). On success a hook will print exactly the worktree path and a newline.
- The command dispatcher every command plugs into: a table of commands, each with its own flags
  accepted before or after its arguments (`upool take x --json`) and ending at `--`, and `--json`
  on every command, including for a bad flag. Usage lists only the commands that work, so it is
  unchanged for now.
- `internal/unity/fake`, the fake `unity` tests run in place of the Unity CLI: the test binary
  itself, which `fake.Main` turns into the CLI when it runs as `unity`. `fake.Install` puts a
  `unity` symlink to it first on a PATH for the code under test, with a script of replies, each a
  capture from `testdata/captures/unity/` with its variables filled, an exit code and optional
  stderr. Each call logs its arguments, `UNITY_PROJECT_PATH` and working directory, which
  `Calls` returns, and takes the next reply; a call beyond the script fails the test. It never
  chooses an Editor. Nothing uses it yet.
- `internal/testenv`, the harness later tests run upool in: `New(t, opts...)` makes a temporary
  HOME (holding only an empty `.config`) and, beside it, a git repository with one commit on
  `main` holding a fake Unity project: `ProjectVersion.txt` (`6000.4.11f1`), two scripts under
  `Assets/Scripts`, a `ProjectSettings.asset` with a `scriptingDefineSymbols` line, a gitignored
  `Library/` whose `ScriptAssemblies` are newer than every script, and gitignored `CLAUDE.md`,
  `AGENTS.md`, `.claude/settings.local.json` and `.agents/skills/x` with the relative
  `.claude/skills/x` link. `.claude/worktrees` is not ignored, which is `upool init`'s job. Options
  give a stale Library, an open Editor (`Library/EditorInstance.json`), uncommitted changes, and
  a second commit on a `feature` branch so HEAD is one commit past `main`. Paths are canonical;
  `Getenv`, `Setenv` and `Environ` hold the environment (HOME, this process's PATH, no system git
  config and a fixed git identity), and `SetMtime`, `WriteFile` and `Git` change the project.
  Nothing uses it yet.
- `docs/manual-gates/M0.md`, M0's manual gate, with its first check, "Capture the M1 replies":
  the commands that record `unity --version`, `unity cache key` and `claude --version` as new
  captures for M1's `doctor` and staleness checks, and what each must show. `testdata/README.md`
  lists the three captures as pending until a human runs the check and adds them.
- `docs/manual-gates/M0.md`, M0's other four checks, run in order after the first: with the real
  Unity CLI and Hub installed, `go test -race -shuffle=on ./...` passes and starts no Unity
  process; the darwin tests in `internal/paths`, `internal/proc` and `internal/statefile` pass on
  the home volume, which `internal/paths` reports case-insensitive; the tests leave `~/.upool` and
  `~/.config/upool` as they found them; and the M1 captures are committed, with
  `make check-paths check-captures` passing. A last section says what to record on the Build plan
  issue.
- `upool init [--json]`, run inside a Unity project's git repository, registers the project as a
  pool without cloning a slot yet. It refuses, changing nothing, outside a git repository, when
  `ProjectSettings/ProjectVersion.txt` is not at the repository's root (naming a project it finds
  in a subdirectory), when git tracks anything under `Library/`, and while an Editor has the
  project open (`Library/EditorInstance.json` or `ProtocolInstance.json` present). Otherwise it
  adds `.claude/worktrees/` to `.gitignore` unless git already ignores it; takes `base` from
  `UPOOL_BASE`, `.upool.toml` or `~/.config/upool/config.toml`, or else uses origin's default
  branch, or else the current branch, and says which; writes `.upool.toml` with `ready`,
  `max_slots` and `base` if there is none; creates `~/.upool/pools/<repo-key>/state.json` with no
  slots, keeping one already there for the repository; and adds the repository to
  `~/.upool/registry.json`. A repository already registered is reported, and init exits 0.
  Usage now lists it.
- `upool init` refuses a stale Library, changing nothing: when `Library/ScriptAssemblies` is
  missing, or a `.cs` file under `Assets/` is newer than the newest assembly there. The message
  names the script and both times, and says to open the project in Unity, let it finish importing
  and compiling, and quit it before running init again. Uncommitted changes do not count as stale.
  `internal/clone` holds the check, which also reads the checkout's HEAD and whether
  `git status --porcelain` is empty, for the clone recipe to record later.
- `upool init` clones the pool's first slot, `slot-1`, from the main checkout once its checks pass,
  and before it registers the repository, using the clone recipe in `internal/clone`
  (docs/contracts.md (g)). The recipe writes the slot to `state.json` as `creating`, owned by the
  running process, before `git worktree add --no-checkout --detach` at the main checkout's HEAD.
  It then runs `/bin/cp -cR` on every top-level entry except `.git`, `Temp`, `Logs`, `obj` and
  `Builds`, and copies `.claude` without `worktrees`. It strips `Library/EditorInstance.json`,
  `ProtocolInstance.json` and `Library/Pipeline`, and runs `git reset -q`. If the main checkout
  had uncommitted changes it reverts them in the slot with `git checkout -- .` and `git clean -fd`,
  and it checks out `base` detached if HEAD is elsewhere. It marks the slot `ready`, recording
  `seed_head` and `seed_dirty`, only once the slot's status is empty, its HEAD is `base`, its
  `.git` file points into `<repo>/.git/worktrees/`, and `git worktree list` lists it. The
  `.gitignore` line and `.upool.toml` that init itself writes are among the changes reverted, so
  the slot has neither until they are committed to `base`. A clone that fails part-way leaves the
  slot `creating`. A pool that already has slots, as after a lost registry, keeps them and gets no
  new one; init lists them. init now also refuses, changing nothing, a `base` that names no commit,
  such as origin's default branch with no local branch of that name. Cloning needs macOS: elsewhere
  init fails when it reaches the clone.
