# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- `upool setup` writes `~/.config/upool/config.toml`, a comment naming every key, when there is
  none. It refuses when no `upool` on `PATH` is the binary running, and when `HOME` is in a
  `Documents`, `Desktop` or `Downloads` folder, where the desktop app's hook helper could not read
  the state the hooks need under `~/.upool`. A second run rewrites only what changed and says what
  it did to each file; `--json` reports the same. The hook handlers are still stubs.
- `upool setup` registers upool itself at user scope in `~/.claude/settings.json`, for
  `WorktreeCreate`, `WorktreeRemove` and `SessionStart`: one matcher group each, with no matcher,
  whose handler runs `upool hook <event>` in exec form (`"args": ["hook", "<event>"]`, so no shell
  splits upool's path), by the path this binary was found through on `PATH`, symlinks kept, so a
  Homebrew install's `/opt/homebrew/bin/upool` survives `brew upgrade`. No script stands between the
  hook and upool: the desktop app's helper runs a binary under `~/Documents`, though it cannot read
  a file there, so a upool built in a checkout there works (#163; docs/contracts.md A24). Every
  other key, group and hook in the file is kept, in the order written. upool's own earlier entries,
  any handler that runs `upool hook` by any path, in exec or shell form, are replaced where the
  first of them was rather than duplicated, so a second run leaves the file as it was. The write is
  atomic, keeps the file's mode (0600 for a new one) and goes through a link to the file it links
  to. Before its first change to an existing file, setup keeps a copy, `settings.json.upool-backup`,
  which no later run replaces. A settings.json that is not JSON, or whose `hooks` is not an object
  of arrays, is refused before setup writes anything. Project settings are never written.
- `upool setup` installs the upool skill, `assets/skill/upool/SKILL.md`, at
  `~/.claude/skills/upool/SKILL.md` and `~/.agents/skills/upool/SKILL.md`, and upserts upool's
  marked block into `~/.claude/CLAUDE.md` and `~/.codex/AGENTS.md`, creating each file and its
  directory when absent. The skill is short and operational: pass `UNITY_PROJECT_PATH` positionally
  to `unity test`, `run`, `build`, `close` and `cache key`; `save_all` before `close`; no
  hand-edited scene YAML while a live Editor is reachable; re-query after a timeout before
  retrying; open your scene explicitly after a headless session; and a subagent that needs Unity
  takes its own slot with `upool take` and releases it. A copy of the skill that differs is
  rewritten. The block is upserted as `init` does it, every byte outside its markers kept, and an
  instruction file whose markers do not pair is refused before setup writes anything.
- `upool doctor` checks the hook installation, which belongs to the machine rather than the pool:
  `hook-settings` fails unless `~/.claude/settings.json` registers `upool hook <event>` for each
  event, in exec form with upool an absolute path, and each upool it registers is an executable
  file, wherever it is; and `hook-provenance` warns about each registered repository whose
  own `.claude/settings*.json` also registers upool's hooks, which the desktop app does not vouch
  for there.
- `upool run <name> -- <cmd...>`, the agent-agnostic launcher: it takes a slot as `take <name>`
  does, runs the command in it with the slot as its working directory and `UNITY_PROJECT_PATH`
  (and `PWD`) set to it, forwards SIGINT and SIGTERM to it, releases the slot when it exits with
  `--if-lease-id` of its own lease (clean → ready, dirty → parked), and exits with the command's
  status, or 128 plus the signal's number when a signal ended it. The lease's holder is `run`'s
  own process, so a `run` that is killed leaves a lease the next `take` can reclaim; off macOS,
  where process start times are unsupported, the lease has no holder. The command keeps upool's
  standard streams; `run` says what it took and released on stderr. A command that does not
  start fails `run`, and its slot is still released.
- `upool hook worktree-create` decodes the request on stdin leniently and tells a subagent's,
  by `prompt_id`, from a session's, desktop or CLI by `transcript_path` (docs/contracts.md (a)).
  A subagent gets an ordinary worktree, detached at the repository's HEAD, at
  `<cwd>/.claude/worktrees/<name>`, as Claude Code would have made it; if `cwd` is in a
  registered pool, matched as A13 says, the pool's `state.json` records it under
  `agent_worktrees` with its parent session and the hook's parent PID, and a worktree that cannot
  be recorded is removed again. The hook prints only the path. `registry.Containing` is the A13
  match: a registered main checkout or any directory in it but its `.claude/worktrees`, the
  innermost of nested ones.
- `upool hook worktree-create` routes a session's request by its `cwd`, matched as A13 says. A
  session in a repository upool does not manage, or in one of a pool's slots, gets the
  pass-through worktree, `git -C <cwd> worktree add <cwd>/.claude/worktrees/<name>` on a branch
  named `<name>`, as Claude Code would have made it, and the hook prints its path; it opens no
  file in the repository, since the desktop app's hook helper could run only git and stat paths
  under `~/Documents`.
- `upool hook worktree-create` gives a session in a pool one of its slots: the first ready slot, or
  else a `needs-reset` one (A5), that is still on disk and in `git worktree list`, leased
  unconfirmed with the stdin `session_id` as holder (any `local_` prefix stripped) and its `name` as
  `name_hint`. It locks the slot's worktree for the lease, prints only the slot's path, and first
  ends the pool's unconfirmed leases older than `confirm_timeout`, releasing their locks. With no
  slot free it tries again once a second for 20 s (A11), then exits 1, which aborts the session,
  with a message naming `upool refresh`. It opens no file under the repository: slots are checked by
  a stat and git alone, and `confirm_timeout` comes from the user file, the environment or the
  default, never from `.upool.toml`. A lock git refuses is said on stderr, and `take --confirm`
  takes it instead.
- `upool hook worktree-remove` ends a slot's lease when the desktop app archives its session: it
  finds the slot by its canonical path, which the app may hand back lowercased, judges it with git
  reads alone (`git status --porcelain`, and `git for-each-ref --contains HEAD`, so a commit on no
  branch is dirty), and marks a clean slot `needs-reset` and a dirty one `parked`, each status line
  on stderr, clearing the lease and setting `last_used`. Define-order noise counts as clean but is
  left for the deferred reset, as is the slot's worktree lock: git writes from the app's hook
  helper are untested. The hook exits 0 whatever happens, its errors on stderr.
  `pool.DirtyBesideNoise` is the clean test without the revert.
- `upool hook worktree-remove` removes a pass-through worktree, any worktree that is no pool's
  slot, when it is clean as a slot would be: `git -C <cwd> worktree remove <worktree_path>`,
  without `--force`, after reverting define-order noise, so git still refuses anything else. Its
  branch and commits stay. A dirty one is kept, with a line on stderr per reason, and a path that
  is already gone is nothing to remove; the hook exits 0 every way (A22).
- `internal/pool` gains the create hook's take, `UnconfirmedTake`, for the hook's pooled branch
  (#78): it hands out a ready slot, or else a `needs-reset` one (A5), never clones, and leases it
  unconfirmed with no holder PID and the request's name as `name_hint`. A lease on a `needs-reset`
  slot is marked with the new `reset_pending` key, the maintainer's answer to the question #75
  raised (A23).
- An unconfirmed lease older than `confirm_timeout` (default 60 s) now ends whenever `take`,
  `status` or `doctor` reads its pool: its slot goes back to `ready`, or to `needs-reset` when
  `reset_pending` says its deferred reset never ran, its git worktree lock is released, and a
  line says so (a warning from `status` and `doctor`, progress from `take`). `status` and
  `doctor` therefore write `state.json` when they end a lease, and only then. state.json gains
  `reset_pending` in every lease; an older upool refuses the unknown key.
- `upool take --confirm <path>` confirms the create hook's lease on the slot at path, matched by
  its canonical path, as the SessionStart hook will run it: it takes the lease's git worktree lock
  if the take did not, runs the deferred reset of a slot handed out from `needs-reset` (define-order
  noise reverted, `git checkout --detach <base>`, `git clean -fd`, the cache key compared across
  it), makes the lease `leased` and confirmed with upool's parent, the session's CLI, as its
  holder, and replaces the slot's `CLAUDE.md`, `AGENTS.md`, `.agents/` and `.claude/` (all but
  `.claude/worktrees`) with the main checkout's, copied with `cp -cR`, so an entry the main
  checkout lacks goes and relative links stay relative. A confirmed lease confirms again and
  re-copies again. It prints the slot's path; a path that is no leased slot exits 1, and so does
  a slot owed a reset that is not clean, with nothing reset.
- `upool hook session-start` reads the session's slot from stdin `cwd`, matched by canonical path,
  and does nothing for a session in no slot. On `startup` it runs `take --confirm`'s confirm
  in-process, then appends `export UNITY_PROJECT_PATH='<slot>'` to the file `CLAUDE_ENV_FILE` names,
  once however often it runs for the session. On `resume`, `clear` and `compact` it exports the
  variable and, while the slot has a lease, records its parent, the session's CLI, as the lease's
  holder, re-copying no agent file and running no reset (A14). It exports the slot even when the
  confirm fails, prints nothing on stdout, which Claude Code adds to the session's context, and
  exits 0 on every path, with any failure on stderr.

## [0.1.0] - 2026-10-20

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
  missing, or a `.cs` file under `Assets/` is newer than the Editor's last compile, the newest
  assembly in `Library/ScriptAssemblies` or in the build cache's Editor DAG,
  `Library/Bee/artifacts/<hash>E….dag/`, whichever is newer. The build cache counts because the
  Editor copies an assembly into `ScriptAssemblies` only when its content changed, so a pull or
  branch switch that gave scripts new mtimes and no new content left init refusing after any
  number of refreshes (#144); a player build's DAG does not count. The message names the script,
  the assemblies it compared against, and both times, and says to open the project in Unity, let it finish importing
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
- `upool which [--json]` prints the path of the slot containing the working directory, in any
  registered pool, comparing canonical paths, so `/var` and `/private/var` spell the same slot.
  `--json` gives the slot's `name`, `path` and `state`, its `lease` (`holder`, `lease_id`,
  `confirmed`, `acquired_at`) or `null`, and the pool's `repo`. Anywhere else, including the main
  checkout and an agent worktree, it exits 1 with `NOT_IN_SLOT`. It reads `registry.json` and
  `state.json` under `~/.upool/lock` and writes neither; it takes the lock only when a registered
  main checkout contains the working directory, so it never creates `~/.upool`. It refuses
  arguments. Usage now lists it. `internal/registry` gains `List`, every registered repository.
- `upool status [--all] [--json]` shows the pool containing the working directory (from its main
  checkout, anywhere under it, or inside a slot): a line naming the main checkout and its base,
  then a table of its slots with their state, lease holder, lease age (two units, such as `45s`,
  `1h5m` or `2d3h`) and whether the lease is confirmed, `-` where a slot has no lease. `--all`
  shows every registered pool, in the order they were registered, separated by blank lines, and
  says `no pools are registered` when there are none. `--json` gives `pools`, each with `repo`,
  `base` and `slots`, each slot with `name`, `path`, `state` and the `lease` `which` reports, or
  `null`; one pool and `--all` have the same shape. Outside every registered pool, status without
  `--all` exits 1 with `COMMAND_FAILED`. Like `which`, it compares canonical paths, writes nothing
  and never creates `~/.upool`. Usage now lists it.
- `upool init` adds upool's instruction block to the repository's `CLAUDE.md`, creating the file
  if there is none: take a slot with upool, never `git worktree add`; `upool which` finds your
  slot; `UNITY_PROJECT_PATH` is exported and passed positionally to `unity test`, `run`, `build`,
  `close` and `cache key`; `unity command save_all` before `unity close`. The block sits between
  `<!-- upool:begin v1 -->` and `<!-- upool:end v1 -->` lines and is appended after a blank line.
  A later init replaces a copy of the same or an older version in place, keeps one of a newer
  version, and leaves every byte outside the markers as it was; a symlinked `CLAUDE.md` stays a
  link. A `CLAUDE.md` whose markers do not pair into one block is refused, naming the lines,
  before init writes anything. `--json` reports `claude_md_block`: `created`, `inserted`,
  `replaced`, `unchanged` or `kept_newer`. A tracked `CLAUDE.md` that init changes is reverted in
  the first slot with init's other uncommitted edits until the block is committed. The block's
  text is `assets/claude-md-block.md`, embedded by the new `assets` package, and the upsert is
  `internal/block`.
- `internal/pool`: the define-order noise rule. `Noise` reports whether a worktree's only change is
  the Editor reordering `scriptingDefineSymbols`: `ProjectSettings/ProjectSettings.asset` is the
  only path `git status` reports, modified in the worktree and not staged, every changed line is an
  entry of its `scriptingDefineSymbols` mapping, and each platform has the same symbols in another
  order. A define added, removed or replaced, any other changed line, spacing or indentation in that
  file, a staged change, or a change or untracked file anywhere else is not noise. `RevertNoise`
  checks that one file out when it is noise and changes nothing otherwise. `internal/git` gains
  `DiffSides`, the index's and the worktree's versions of a file read through one full-context
  `git diff` (so the rule needs nothing but git, as the remove hook will), and `CheckoutPath`.
  Nothing calls them yet; `release` and the remove hook will.
- `upool doctor [--json]` checks the pool containing the working directory, found as `status`
  finds it, and prints the main checkout and then one line per check: `pass`, `warn` or `fail`,
  its name and a one-line reason. Any failure exits 1 with `COMMAND_FAILED` naming the failed
  checks; a warning alone exits 0. `--json` gives `repo` and `checks`, each with `name`, `result`
  and `reason`. The checks are:
  - `gitignore`: git ignores `.claude/worktrees`.
  - `library`: git tracks nothing under `Library/`.
  - one per slot: the slot is a directory on disk, `git worktree list` lists it, and its `.git`
    points back into the main checkout's `.git/worktrees/`. `creating` slots are skipped.
  - `clone-sharing`, on macOS only: `.claude/worktrees` is on APFS and on the main checkout's
    device, and a `cp -c` clone of a 128 MB scratch file there costs less than half of it in free
    space: the median of three clones, so that one disturbed by other disk activity does not
    decide. Every scratch file is removed afterwards.
  - `state-volume`: warns when `~/.upool` resolves into `~/Library/CloudStorage` or
    `~/Library/Mobile Documents`, or, on macOS, sits on a network volume, where `flock` is advisory.
  - `leases`: warns about a lease with no holder PID older than `agent_ttl`.

  Outside every registered pool, doctor exits 1. `internal/paths` gains `StatVolume` (macOS), and
  `internal/clone` gains `ProbeSharing` (macOS) and exports `CheckBacklink`. Usage now lists
  doctor.
- `upool doctor --fix` repairs what is safe to repair. A `creating` slot is now checked by its owner
  process. It passes while that process is alive at its recorded start time, and fails as an
  interrupted clone once the process is gone. `--fix` removes an interrupted clone: its worktree
  registration with `git worktree remove --force` if git lists it, its directory, and its entry,
  refusing any path outside `.claude/worktrees`. A `ready` slot with no lease whose directory is
  gone fails, as before, and the reason now says `--fix` drops it. `--fix` runs
  `git worktree prune` and drops the entry, so that a later clone replaces it. A fixed check
  passes with a reason that starts `fixed:`. `--fix` never touches a leased, unconfirmed, parked
  or `needs-reset` slot, nor a slot whose directory is still on disk. `internal/git` gains
  `WorktreeRemove` and `WorktreePrune`.
- `upool doctor` checks the Unity CLI's and Claude Code's versions. It runs `unity --version` and
  `claude --version`, found on the caller's `PATH` and bounded at 10 s each, and reads the version
  as the captures print it (`1.0.0-beta.13`, `2.1.299 (Claude Code)`). The checks `unity-cli` and
  `claude-code` fail below 1.0.0-beta.13 and 2.1.295, compared by semantic-versioning precedence:
  beta.12 is below beta.13, beta.100 above it, and 1.0.0 above every 1.0.0 prerelease. They also
  fail when the version cannot be read or `--version` fails, and warn when the tool is not on
  `PATH`. The fake `unity` in tests (`internal/unity/fake`) now also answers as `claude`, from
  `testdata/captures/claude/` (`InstallClaude`), and `Reply.Replace` replays a copy of a capture
  with a value substituted.
- `upool take <name> [--base REF] [--holder ID] [--timeout S] [--json]` leases a ready slot of the
  pool containing the working directory and prints its path and nothing else. `--json` gives
  `path`, `slot`, `branch`, `lease_id` and `repo`. `<name>` is a branch: take checks it out in the
  slot, creating it from `--base`, or the pool's base, when it does not exist. A name git would
  refuse is refused first. So is a branch git already has checked out in a worktree, the main
  checkout included: the error names that worktree, and nothing is leased.
  - **Choosing a slot.** Under `~/.upool/lock`, take hands out the first `ready` slot whose
    directory exists, which `git worktree list` lists, and whose backlink checks out.
  - **The lease** gets a random `lease_id`, `holder` from `--holder`, the slot's
    `worktree_path`, no holder PID, `acquired_at`, `confirmed` true and `lane` `script`. With
    the lock dropped, take runs `git worktree lock --reason "upool lease <id>"`. If the lock or
    the checkout fails, the lease is undone.
  - **When no slot is ready,** take prints each slot's skip reason to stderr: `creating`,
    `warming`, `leased`, `unconfirmed`, `parked`, `needs-reset` or
    `missing-from-worktree-list`. It retries once a second until `--timeout` seconds
    (default 0) have passed, then exits 1.

  take does not clone a slot yet. Usage now lists it. `internal/git` gains `CheckBranchName`,
  `BranchExists`, `CheckoutBranch`, `CheckoutNewBranch`, `WorktreeLock` and `WorktreeUnlock`.
- `upool path <name> [--json]` prints the path of the slot that has the branch `<name>` checked out,
  found in `git worktree list` in the main checkout of the pool containing the working directory
  and compared by canonical path, so `/var` and `/private/var` spell the same slot. `--json` gives
  what `which` gives for that slot. With no slot on the branch, including when only the main
  checkout or a worktree that is not a slot has it, path exits 1 with the new `NO_SUCH_SLOT` code,
  naming the worktree that has it. It writes nothing. Usage now lists it.
- `upool release [path|name] [--if-lease-id ID] [--force] [--json]` returns a leased slot to the
  pool.
  - **The target** is a path (an argument naming an existing directory), a branch checked out
    in a slot, or the slot containing the working directory.
  - **Refusals.** It refuses a slot that is not leased. It refuses a lease whose holder process
    is alive unless `--if-lease-id` names that lease or `--force` is given. With `--if-lease-id`
    naming another lease, it changes nothing and exits 1.
  - **Clean or dirty.** It reverts define-order noise, then judges the slot clean as
    docs/contracts.md (f) defines it.
  - **A clean slot** is checked out at base detached and gets `git clean -fd` (never `-x`, so
    the ignored `Library/` stays). It is unlocked and marked `ready` with its lease cleared and
    `last_used` set. The branch it was on is kept, commits and all.
  - **A dirty slot** is parked: unlocked, lease cleared, and nothing in it changed. A warning on
    stderr names each `git status --porcelain` line, or "HEAD is on no branch".

  `--json` gives `slot`, `path`, `state`, `repo` and, for a parked slot, `dirty`. Usage now
  lists it.
- A lease whose holder process died is reclaimed. A clean slot gets the release reset and is
  marked `ready`; a dirty one is `parked` and unlocked, with nothing in it changed.
  - **When:** `upool take` reclaims before each try at choosing a slot, and says on stderr what
    it reclaimed or parked. `upool doctor --fix` reclaims too, and reports `fixed:` on the
    slot's line.
  - **Reporting only:** without `--fix`, doctor warns on the slot's line that the next take or
    `--fix` reclaims it. `status` reclaims nothing.
  - **What counts as dead:** a holder is dead when `internal/proc` finds no process with its PID
    at its recorded start time, so a reused PID counts as dead.
  - **What is never reclaimed:** a lease with no holder PID (a bare `take`), a lease with no
    recorded start time, and a slot that `~/.upool/editors.json` shows a live Editor in.

  `internal/pool` gains `Reclaimer`, and `Dirty`, `ResetToBase` and `Unlock`, the reset that
  release and the reclaim now share.
- `upool take` clones a slot when it can hand none out, `--no-create` was not given, no slot is
  `needs-reset`, and the pool is below `max_slots`. It then leases the new slot.
  - **The seed** is chosen as docs/contracts.md (g) step 0 and A19 say: the first candidate with
    no live Editor (by `~/.upool/editors.json` or the Editor's instance files) whose Library
    passes the staleness check. Candidates come in this order: the freshest ready slot (the one its
    Editor compiled last, as the staleness check reads it), a `needs-reset` slot after its reset, then the main checkout.
  - **A slot seed** is held `warming`, owned by take, while it is copied, then returns to
    `ready`.
  - **The clone** releases the lock during the copy and reports progress on stderr.
  - **No clone:** with no seed qualifying, or at `max_slots`, take says why on stderr once and
    waits or fails as for a full pool.

  `--no-create` keeps the wait-or-fail behaviour. `internal/clone` gains `Seeder` (`Choose`,
  `Done`, `ErrNoSeed`). `Recipe` gains `MaxSlots`, which refuses a clone past `max_slots` under
  the lock with `ErrPoolFull`. `internal/ledger` gains `LiveIn`, which the reclaim now uses too.
- The clone recipe's step 7 and `release`'s reset compare the slot's `unity cache key` before and
  after the checkout.
  - **The call** is `unity cache key "<slot>" --format json`, with the path positional and never
    `--project-path`, which `cache key` rejects.
  - **A changed key** sets the slot's `reimport_pending` and warns that its next open is a large
    reimport, on stderr from `take` and in the warnings of `init` and `release`.
  - **A key that cannot be read** leaves `reimport_pending` as it was, with a warning.
  - **No `unity` on `PATH`:** nothing is compared, and nothing is said, since `doctor` reports a
    missing Unity CLI.

  `internal/unity` gains `CacheKey`, this one verb's entry point until M3's verb table.
  `internal/clone` gains `KeyCheck` and `Recipe.Keys` and `Recipe.Warn`. `internal/testenv` now
  puts only git on its `PATH`, so that no test reaches this machine's Unity CLI.
- `upool init --slots N` clones N slots now, from 1 to `max_slots`.
  - **Seeds:** slot-1 comes from the main checkout, and the rest are seeded as A19 says, normally
    from slot-1, which is held `warming` while it is copied.
  - **Config:** it writes nothing about N to `.upool.toml` or the user config; keeping slots
    ready is `ready`'s job.
  - **Refused, before init writes anything:** `--slots` outside 1 to `max_slots`.
  - **Output:** the human form names each slot's seed. `--json` now gives `slots`, a list, each
    with its `seed`, in place of `slot`.

  Every command now answers `-h` and `--help` with its usage line and flags on stdout, exiting 0;
  `init`'s says that slots beyond `ready` become `prune` candidates once idle for `shrink_after`.
- `docs/manual-gates/M1.md`, M1's manual gate. Seven checks, run in order with the spike's
  project on the spike's Mac:
  1. `upool init` clones slot-1 in about 26 s for under 100 MB, clean and with the agent files.
  2. slot-1's first batch open imports about one asset and compiles nothing.
  3. `status`, `take`, `which`, `path` and `doctor` agree.
  4. `release` resets a clean slot, keeping the branch's commit and the Library, and parks one
     with an untracked file.
  5. `take` clones nothing while the main checkout's Editor is open, and clones slot-2 from it
     once it is closed.
  6. `release` parks a commit on no branch.
  7. `doctor` reports a clone interrupted with Ctrl-C, and `doctor --fix` removes it.

  Sections before and after say how to start, what to record on the Build plan issue, when to
  tag `v0.1.0`, and how to resolve the two slots the gate leaves parked.
