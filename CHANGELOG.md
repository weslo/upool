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
