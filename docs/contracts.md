# upool contracts

The external behaviour `upool` depends on, extracted from [design.md](design.md) and
[spike-report.md](spike-report.md). Every row cites its source. Where the two documents disagree,
the spike report wins and a footnote says so. Fixture names refer to files under
[`testdata/captures/`](../testdata/README.md), except those starting `evidence/`, which are under
[`docs/evidence/`](evidence/README.md). Nothing here is new design: gaps are listed under
[Open questions](#open-questions), not filled.

Citation keys:

| Key | Section |
|---|---|
| D-intro | design.md, opening section "Clone the Checkout, Lease Managed Slots, Budget Editors by RAM" |
| D-commands | design.md, "One binary, a dozen commands, one JSON file per pool" |
| D-clone | design.md, "Why clone the whole checkout rather than check out and clone the Library" |
| D-lifecycle | design.md, "Slot lifecycle: seed only from a Library current at HEAD, refresh on handout, reset without `-x`" |
| D-disk | design.md, "Disk: 0.4 GB per persistent slot; disk is not what limits the count" |
| D-editors | design.md, "Editors: budget by RAM, serialize cold imports as a courtesy, target by variable and by path" |
| D-route | design.md, "Route Claude Code's worktrees through the pool: three branches in one hook" |
| D-env | design.md, "Environment, confirmation and release" |
| D-leases | design.md, "Leases that survive crashes and never reclaim a live tree" |
| D-lanes | design.md, "Lanes: scripts in parallel, scenes one at a time, `.meta` never regenerated" |
| D-unverified | design.md, "What remains unverified" |
| D-build | design.md, "Build in four phases, with the clone recipe first" |
| S§n X | spike-report.md section n, step X (for example S§8 A.7 is §8, row A.7) |

## (a) WorktreeCreate and WorktreeRemove stdin

### WorktreeCreate fields by request kind

| Field | Desktop session | CLI `--worktree` session | Subagent | Source |
|---|---|---|---|---|
| `session_id` | UUID of the app's session (the app's log writes it `local_<uuid>`)[^1]; differs from the same session's `SessionStart` `session_id` | CLI session UUID; equal to that session's `SessionStart` `session_id` | the parent CLI session's UUID; same for every subagent of that session | S§2 TB; S§8 "A, session ids"; S§5; S§9 F.3 |
| `transcript_path` | `""` | set, keyed by the repo path | set, the parent's, keyed by the repo path | S§9 "How to tell the requests apart" |
| `cwd` | main checkout | main checkout | main checkout | S§5; S§9 F.3 |
| `hook_event_name` | `"WorktreeCreate"` | `"WorktreeCreate"` | `"WorktreeCreate"` | S§5 |
| `name` | generated from the first prompt plus a 6-hex suffix (`env-project-status-6b702e`); a hint only | user-given (`probe`) | `agent-` plus 17 hex (`agent-a59f8e76142da8348`) | S§7 item 4; S§9 F.3 |
| `scratchpad_dir` | absent | absent | present | S§9 F.3 |
| `prompt_id` | absent | absent | present; equal across subagents of one prompt | S§9 F.3 |
| `agent_id`, `agent_type`, `worktree_name` | absent | absent | absent | S§2 T7.5; S§9 F.3 |
| Process running the hook | parent `Claude.app/Contents/Helpers/disclaimer`, grandparent `Claude.app/Contents/MacOS/Claude`; under `~/Documents` only `ls -d` and `git` worked | the CLI | the bundled CLI binary (`…/claude.app/Contents/MacOS/claude`), full file access | S§2 TB; S§9 F.2–3 |
| `CLAUDE_ENV_FILE` | unset | not recorded | not recorded | S§7 (desktop probe log) |
| Fixtures | `hooks/desktop-session-create.json`, `-refused`, `-reuse`; `hooks/sibling-*-create.json`; `hooks/desktop-probe-create.json` | `hooks/cli-session-create.json` | `hooks/subagent-create.json`, `hooks/subagent-create-2.json` | testdata/README.md |

### Telling the kinds apart

| Kind | Distinguishing field | Test | Source |
|---|---|---|---|
| Subagent | `prompt_id` | present; corroborated by `scratchpad_dir` present and `name` matching `agent-<17 hex>` | S§9 F.3; S§9 "What this changes (updated)", subagent row; D-route |
| Desktop session | `transcript_path` | `""` and no `prompt_id` | S§9 "How to tell the requests apart" |
| CLI `--worktree` session | `transcript_path` | non-empty and no `prompt_id` | S§9 "How to tell the requests apart" |

The hook's three branches (D-route) key on subagent versus not, then on whether `cwd` matches a
repository in `~/.upool/registry.json`: subagent → `git worktree add --detach
<repo>/.claude/worktrees/<name>`; pooled session → `upool take --no-create --holder <session_id>
--timeout 20`; anything else → pass-through `git -C <cwd> worktree add <cwd>/.claude/worktrees/<name>`.
Desktop versus CLI does not change the branch; it is why leases correlate by worktree path, never
by `session_id` (S§7 item 3; D-route).

### WorktreeCreate output and app behaviour

| Rule | Source |
|---|---|
| stdout carries the worktree path and nothing else; every other byte goes to stderr. The app treats a path "selected from multi-line output" as not vouched | D-route; D-commands; `evidence/logs/desktop-probe-main.log` |
| Any non-zero exit aborts the session with `worktree_hook_failed`; there is no fallback to the app's own creation | S§2 T7.6; S§8 A.7; S§8 "What this changes (updated)", hook scope row |
| A path held by a live session is refused: `WorktreeCreate hook failed: hook printed "<path>", which is already in use by worktree "<name>"`, then `worktree_hook_failed`; no `WorktreeRemove` follows a refusal | S§8 A.7; S§9 E.7; `evidence/logs/sibling-slot-main.log`, `evidence/logs/managed-slot-main.log` |
| Only a path inside `<repo>/.claude/worktrees` gets the config overlay and auto-trust ("Auto-trusted worktree … (inherited from <repo>)"); elsewhere the log says "hook returned a path outside the managed worktree directory" | S§8 A.5; S§9 E.5; `evidence/logs/managed-slot-main.log`, `evidence/logs/sibling-slot-main.log` |
| The overlay copies only files the slot lacks ("Skipped 3 gitignored .claude files already present") and skips links to directories (21 under `.claude/skills`) | S§9 E.5; S§10 |
| The hook must be registered at user scope (`~/.claude/settings.json`, exec form); from a project settings file the app logs "hook came from a repo-committed settings tier … (provenance not vouched)" | S§2 TB; S§8 A.2–3, A.5; `evidence/logs/desktop-probe-main.log` |
| The hook script must live outside `~/Documents`: under it, `getcwd` and `bash` failed with `Operation not permitted` | S§2 T7.6; S§7 |
| Handing out a pre-made slot took `worktree=36ms` to `112ms`, against `4634ms` for a `git worktree add` inside the hook | S§8 A.5; S§9 E.5; `evidence/logs/sibling-slot-main.log`; `evidence/logs/desktop-probe-main.log` |
| A user-scope create hook runs for every repository and every subagent; there is no matcher | S§9 "What this changes (updated)", blast radius row; D-route |

### WorktreeRemove fields

| Field | Desktop session | CLI `--worktree` session | Subagent | Source |
|---|---|---|---|---|
| `session_id` | same value as the matching create | not captured | never fires | S§8 A; S§9 E.6 |
| `transcript_path` | `""` | not captured | never fires | S§8 A |
| `cwd` | main checkout | not captured | never fires | S§8 A |
| `hook_event_name` | `"WorktreeRemove"` | not captured | never fires | S§2 TB |
| `worktree_path` | the path the create hook printed | not captured | never fires | S§2 TB; S§9 E.6 |
| When it fires | about 1 s after the session is archived | never for `claude -p --worktree`; interactive `claude --worktree` untested | never: not when the subagent finishes, not when the parent session is archived | S§2 T7.5; S§8 A.6; S§9 F.3; D-unverified |
| Fixtures | `hooks/desktop-session-remove.json`, `-reuse`; `hooks/sibling-*-remove*.json`; `hooks/desktop-probe-remove.json` | none | none | testdata/README.md |

| Rule | Source |
|---|---|
| Exit 0 with the directory still present is accepted: the app logs "Removed hook-based worktree", deletes its registry entry and can hand the same path out again | S§8 A.6–A.7; S§9 E.6 |
| Archive gives no warning about uncommitted work; the remove hook saw `?? spike-reuse.txt` | S§8 A.8; `git/remove-status-untracked.txt` |
| `git -C <slot> status --porcelain` works from the disclaimed helper; git writes (`checkout`, `clean`) and `unity` calls from it are untested | S§8 A, `~/Documents` row; D-unverified |

## (b) SessionStart and `CLAUDE_ENV_FILE`

### SessionStart fields

| Field | Desktop, worktree session | Desktop, no worktree | CLI `-p --worktree` | Source |
|---|---|---|---|---|
| `session_id` | CLI session UUID; differs from the create/remove `session_id` | CLI session UUID | same value as the create's | S§8 "A, session ids"; S§5 |
| `transcript_path` | keyed by the slot path (`…/${repo_key}--claude-worktrees-slot-1/…`) | keyed by the repo path | keyed by the worktree path | S§5; S§9 E, added memory row |
| `cwd` | the slot | main checkout | the worktree | S§2 TB; S§5 |
| `scratchpad_dir` | present | present | absent | S§5 (both captures) |
| `hook_event_name` | `"SessionStart"` | `"SessionStart"` | `"SessionStart"` | S§5 |
| `source` | `"startup"` | `"startup"` | `"startup"` | S§5; the only value captured |
| Process | the CLI process; `getcwd` works under `~/Documents` | same | same | S§2 TB |
| Process cwd | the **main checkout**, even when stdin `cwd` is the slot: read stdin `cwd`, never `$PWD` | main checkout | not recorded | S§2 TB; S§7 item 6 |
| Fixtures | `hooks/session-start.json`, `hooks/session-start-reuse.json`, `hooks/sibling-*-start.json`, `hooks/desktop-probe-session-start.json` | `hooks/desktop-no-worktree-session-start.json` | `hooks/cli-session-start.json` | testdata/README.md |

### `CLAUDE_ENV_FILE`

| Rule | Source |
|---|---|
| Set for `SessionStart` hooks to `~/.claude/session-env/<SessionStart session_id>/sessionstart-hook-0.sh` | S§2 T7.5 |
| The hook appends shell lines of the form `export NAME=value`; they reach the session's Bash tool in the desktop app, with and without a worktree[^2] | S§2 T7.5; S§7; S§8 A.5 (`UNITY_PROJECT_PATH=…/ta` seen in the reply) |
| Unset for `WorktreeCreate` | S§7 (desktop probe log) |
| upool's `SessionStart` hook runs `upool take --confirm "$cwd"`, which confirms the lease by path, re-copies `CLAUDE.md`, `AGENTS.md`, `.claude/` minus `worktrees` and `.agents/` from the main checkout, runs the deferred reset if the slot is marked `needs-reset`, appends `export UNITY_PROJECT_PATH=<slot>`, and spawns a detached `upool refresh` | D-env; D-lifecycle |
| Project-level hooks for a slot session come from the **main checkout's** `.claude/settings*.json` and run with the main checkout as cwd; the slot's own copies never fired in five sessions | S§8 A.5; S§9 E, added hook source row |
| `SessionEnd` (best effort, 1.5 s budget) and `CwdChanged` were not exercised | D-env; D-unverified |

## (c) Unity CLI targeting (CLI 1.0.0-beta.13)

| Command | Honours `UNITY_PROJECT_PATH` | Project argument | Source |
|---|---|---|---|
| `command` (list, `eval`, `save_all`, …) | yes; beats cwd; a path with no Editor fails `COMMAND_FAILED: No Pipeline instance found for project: <path>` instead of falling back | `--project-path` also accepted | S§2 T6.4; S§8 B.2–B.3; `unity/command-eval-no-pipeline-instance.json` |
| `status` | yes; the listing is filtered to that project; none running → `STATUS_NO_INSTANCES` | `--project-path` also accepted | S§2 T6.4; S§8 B.4; `evidence/unity-projections/status-env-no-instances.json` |
| `recompile` | yes | — | S§2 T6.4; `unity/recompile-up-to-date.json` |
| `list`, `job`, `mcp` | yes, per the beta.13 release notes; not measured | — | D-intro; D-editors |
| `open` | yes, per the release notes; not measured (the spike always passed the path) | positional `<path>` | D-intro; S§8 C.1; `unity/open-gui.json` |
| `test` | **no**: from `/tmp` with the variable set, `COMMAND_FAILED: Not a Unity project (ProjectVersion.txt not found at /private/tmp).`, exit 6 | positional `[project]`; `unity test <path> --mode EditMode` works | S§2 T6.6; `unity/test-not-a-unity-project.json`, `unity/test-positional.json` |
| `run` | **no**: same error, exit 6 | positional `[project]`; `unity run <path> --command <name>` reaches a live Editor instead of booting one | S§8 B.3; `unity/run-not-a-unity-project.json` |
| `build` | **no**: same error | positional `[project]`; no `--project-path` | S§8 B.3; `evidence/unity-projections/build-not-a-unity-project.json` |
| `close` | not tested with the variable | positional `<project>`, required; hit the right PID with two GUI and with three headless Editors | S§8 B.5, C.6; D-editors; `unity/close-gui.json` |
| `cache key` | `--project-path` is rejected: `INVALID_COMMAND_ARGS: error: unknown option '--project-path'` | positional `[path]`, defaults to cwd | S§8 D.1; `unity/cache-key-project-path-invalid.json` |

Resolution without the variable:

| Editors running | cwd | Result | Source |
|---|---|---|---|
| exactly 1 | anywhere, including `/tmp` and a slot with no Editor | that one Editor: cwd is ignored and the call silently lands in the wrong slot | S§2 T6.4; `unity/command-eval-misrouted.json` |
| 2 or more | inside a running project, including a subdirectory | that project | S§8 B.2 |
| 2 or more | outside every running project (`/tmp`, a slot with no Editor) | `AMBIGUOUS_EDITOR` | S§8 B.2; `evidence/unity-projections/command-eval-ambiguous-editor.json` |
| any | any, with `UNITY_PROJECT_PATH` set | the variable wins | S§8 B.2 (added row) |

Rule for the tool and the skill: export `UNITY_PROJECT_PATH=<slot>` into every session **and** pass
`"$UNITY_PROJECT_PATH"` positionally to `test`, `run`, `build`, `close` and `cache key` (D-editors).

## (d) Exit codes and error codes

### Unity CLI envelope

`{"success": bool, "command": string, "data": object | array | null, "errors": [{"code": string, "message": string}], "warnings": [string]}`
(every full envelope under `testdata/captures/unity/`). Quirks seen in the captures:

| Quirk | Source |
|---|---|
| `command` is not a stable verb: `"status"`, `"command"`, `"command eval"`, `"unity command eval"` (on the failing eval), `"command save_all"`, `"cache"`, `"vcs git worktree add"` | `testdata/captures/unity/*.json` |
| The `recompile` capture has no top-level `errors` or `warnings` | `unity/recompile-up-to-date.json` |
| `unity open` printed `Warning: "<path>" was not found in the Hub project registry, opening as a file path.` before the JSON and repeated it in `warnings` | `unity/open-gui.raw.txt` |
| `vcs git worktree` JSON redacts the home directory to `/Users/<user>`, so its paths cannot be used verbatim (upool never calls it) | S§5 |
| Fixtures write the directory a command ran in as `${cwd}`; the spike ran these from `/tmp`, which the CLI reports as `/private/tmp` | `testdata/README.md` |

### Codes upool must handle

| Exit | `errors[].code` | Message (captured) | When | Source | Fixture |
|---|---|---|---|---|---|
| 0 | — | — | success | S§2; S§8 | `unity/status-*.json`, `close-*.json`, … |
| 6 | `STATUS_NO_INSTANCES` | No Unity Editor instances found with the Pipeline package installed. Open Unity and install the Pipeline package, or run unity pipeline install. | `status --until-ready --timeout N` after waiting the **whole** timeout (15.1 s for 15) for a non-existent path or a project with no Editor | S§2 T6.5; D-editors | `unity/status-until-ready-no-instances.json`, `evidence/unity-projections/status-until-ready-closed-project.json` |
| not recorded | `STATUS_NO_INSTANCES` | same | `status` filtered by the variable or `--project-path` to a project with no Editor | S§2 T6.4 | `evidence/unity-projections/status-env-no-instances.json`, `status-project-path-no-instances.json` |
| 6 | `COMMAND_FAILED` | No Pipeline instance found for project: `<path>`. Make sure Unity Editor is running with the Pipeline package installed. | `unity command` with the variable naming a project that has no Editor | S§2 T6.4 (exit code from the spike's `commands.log`) | `unity/command-eval-no-pipeline-instance.json`, `evidence/unity-projections/command-list-no-pipeline-instance.json` |
| 6 | `COMMAND_FAILED` | Not a Unity project (ProjectVersion.txt not found at /private/tmp). | `test` or `run` with no positional path, outside a project | S§2 T6.6; S§8 B.3 | `unity/test-not-a-unity-project.json`, `unity/run-not-a-unity-project.json` |
| not recorded | `COMMAND_FAILED` | same | `build` with no positional path | S§8 B.3 | `evidence/unity-projections/build-not-a-unity-project.json` |
| not recorded | `COMMAND_FAILED` | Pipeline server returned 400 Bad Request: Internal Server Error. Main thread operation timed out after 5000ms | first `eval` on a freshly opened GUI Editor; **the code ran anyway**, so re-query side effects instead of retrying | S§8 C.3; D-editors | `unity/command-eval-main-thread-timeout.json` |
| not recorded | `AMBIGUOUS_EDITOR` | Multiple Unity Editors are running with Pipeline servers: … Pass --project-path \<path\> with one of the project paths listed above to choose one, or run the command from inside one of those project directories. | 2 or more Editors, cwd outside them, variable unset | S§8 B.2 | `evidence/unity-projections/command-eval-ambiguous-editor.json` |
| not recorded | `INVALID_COMMAND_ARGS` | error: unknown option '--project-path' | `cache key --project-path` | S§8 D.1 | `unity/cache-key-project-path-invalid.json` |
| 7 | — | — | **never observed** in any capture | — | none; see [Open questions](#open-questions) |

### upool's own exit codes

The design says only that "exit codes and the JSON envelope mirror the Unity CLI's own" and that
`which` exits 1 outside a slot (D-commands). These are the codes it pins down; `internal/output`
defines exactly these:

| Code | Constant | Meaning | Source |
|---|---|---|---|
| 0 | `ExitOK` | success | D-commands |
| 1 | `ExitFailure` | the command did not succeed; `upool which` returns it when cwd is not inside a slot | D-commands, command table |
| 6 | `ExitNoTarget` | mirrors the Unity CLI's exit 6: no Editor instance, or no Unity project, at the resolved target | D-commands; S§2 T6.5, T6.6 |

The hook subcommands follow the app's contract rather than this table: `worktree-remove` always
exits 0, and `worktree-create` exits non-zero only to abort the session (D-route).

## (e) State schemas

All three files live under `~/.upool/`, never under a TCC-protected folder, because the create hook
reads them from the disclaimed helper (D-commands). Each is one JSON document rewritten atomically
under one OS file lock, `~/.upool/lock`, shared by all three and held only across the
read-modify-write, never across a 26 s clone or a 235 s import (D-commands). The design names the data but, apart from `lease_id` and `confirmed`,
not the keys; the snake_case names below are descriptive placeholders and the types are the JSON
types the descriptions imply (see [Open questions](#open-questions)).

### `~/.upool/pools/<repo-key>/state.json`

Pool:

| Field | Type | Meaning | Written by | Source |
|---|---|---|---|---|
| `repo` | string, absolute path | the main checkout | `init` | D-commands |
| `base` | string, git ref | the ref slots are cloned at and reset to | `init` (from flag, `UPOOL_*`, `.upool.toml`, user config) | D-commands; D-lifecycle |
| `slots` | array of slot | the pool's slots | see below | D-commands |
| `agent_worktrees` | array of agent worktree | subagent worktrees made by the hook | hook (subagent branch), `prune` | D-route |
| `refresh_job` | object (PID, start time) or null | the one detached top-up job, announced in `status` | `take --confirm` (spawns), `refresh` (clears) | D-build; D-env |

Slot:

| Field | Type | Meaning | Written by | Source |
|---|---|---|---|---|
| `name` | string | `slot-N` | `init`, `take` (create), `refresh` | D-commands |
| `path` | string, absolute path | `<repo>/.claude/worktrees/slot-N` | same | D-commands |
| `state` | string, one of the states in (f) | lifecycle state, including the "acquisition incomplete" entry written before `git worktree add` | every transition in (f) | D-leases; D-route |
| `seed_head` | string, commit SHA | the seed source's `git rev-parse HEAD` at clone time | clone (`init`, `take`, `refresh`) | D-lifecycle; S§8 "What this changes (updated)", staleness row |
| `seed_dirty` | bool | whether the seed source had uncommitted changes | clone | same |
| `last_used` | timestamp | for LRU removal under `max_disk` and for `shrink_after` | `release`, hook (remove) | D-disk; D-commands |
| `lease` | lease or null | current lease | `take`, `take --confirm`, `release`, hooks, reclaim | D-leases |

Lease:

| Field | Type | Meaning | Written by | Source |
|---|---|---|---|---|
| `lease_id` | string, random | identity for `release --if-lease-id` | `take` | D-commands; D-leases |
| `holder` | string | `--holder ID`; for the desktop hook, the create stdin `session_id` | `take` | D-route |
| `worktree_path` | string, absolute path | correlation key for `--confirm`, remove and release | `take` | D-route; D-leases |
| `holder_pid` | integer | process holding the lease | `take` | D-leases |
| `holder_pid_start` | timestamp | that process's start time, so a reused PID is not mistaken for the holder | `take` | D-leases |
| `acquired_at` | timestamp | when the lease was taken; drives `confirm_timeout` | `take` | D-commands; D-leases |
| `confirmed` | bool | `false` when the create hook takes it; `true` after `take --confirm` | `take`, `take --confirm` | D-route; D-env |
| `lane` | string, `script` or `scene` | default `script`; at most one `scene` lease per project | `take --lane` | D-lanes |
| `name_hint` | string | the create stdin `name`; never a key | hook (create) | D-route |

The lease is also held as a `git worktree lock` on the slot for its lifetime; that lives in git, not
in this file (D-leases).

Agent worktree:

| Field | Type | Meaning | Written by | Source |
|---|---|---|---|---|
| `path` | string, absolute path | `<repo>/.claude/worktrees/agent-<17 hex>` | hook (subagent branch) | D-route |
| `parent_session_id` | string | the create stdin `session_id` | hook | D-route |
| `parent_pid` | integer | the creating process's parent PID (the session's CLI) | hook | D-route; D-unverified |
| `created_at` | timestamp | for `agent_ttl` (24 h) | hook | D-commands |

### `~/.upool/registry.json`

| Field | Type | Meaning | Written by | Source |
|---|---|---|---|---|
| `repos` | array of repo | every pooled repository on the machine | `init` | D-commands |
| `repos[].path` | string, absolute path | main checkout; the create hook matches stdin `cwd` against it | `init`, run from Terminal | D-commands; D-route |
| `repos[].key` | string | the `<repo-key>` naming `~/.upool/pools/<repo-key>/` | `init` | D-commands |

The hook reads this file instead of `.upool.toml` because `head` on a file under `~/Documents` failed
from the disclaimed helper (D-commands; S§2 TB).

### `~/.upool/editors.json` (the Editor ledger)

| Field | Type | Meaning | Written by | Source |
|---|---|---|---|---|
| `editors` | array of editor | every Editor `upool` is charging for | `open`, `close`, `release`, `refresh`, `doctor --fix` | D-commands; D-editors |
| `editors[].project` | string, absolute path | the slot the Editor has open | `open` | D-editors |
| `editors[].pid` | integer | Editor PID; a dead PID is reclaimed | `open`; removed by `close`, `release`, `doctor --fix` | D-editors; D-leases |
| `editors[].mode` | string, `headless` or `gui` | how `open` launched it | `open` | D-commands |
| `editors[].opened_by_upool` | bool | `release` closes only Editors the tool opened | `open` | D-commands |
| `editors[].cold_import` | bool | holds a cold-import slot (`max_cold_imports`) while true | `open`, `refresh` | D-editors |
| `editors[].charge_gb` | number | 2.5 when long-lived, 4.5 while a cold import runs | `open`, `refresh` | D-editors |

The helper charge (2 GB once any Editor is up) is computed from the list, not stored (D-editors).

## (f) Slot lifecycle

| State | Meaning | `take` skip reason | Source |
|---|---|---|---|
| `warming` | being cloned (the "acquisition incomplete" entry) or re-warmed by `refresh`[^3] | — | D-leases; D-commands |
| `ready` | detached at base, clean, unleased | — (handed out) | D-lifecycle |
| `leased-unconfirmed` | leased by the create hook, `confirmed: false` | `unconfirmed` | D-route; D-leases |
| `leased` | leased and confirmed | `leased` | D-env; D-leases |
| `parked` | dirty after a remove or release: unleased, not ready, never reset, surfaced by `prune` | `parked` | D-route |
| `needs-reset` | clean after a remove; reset deferred to the next `SessionStart` | `needs-reset` | D-route; D-env; D-leases |

`take` also skips slots for `editor-running` and `missing-from-worktree-list`, which are conditions,
not states (D-leases).

| # | From | To | Caused by | Source |
|---|---|---|---|---|
| 1 | — | `warming` | `upool init` (first slot, from the main checkout with its Editor closed); `upool take` when no slot is ready and the pool is below `max_slots` (not with `--no-create`); `upool refresh` topping the pool up to `ready`, spawned detached by the `SessionStart` hook | D-commands; D-lifecycle; D-env |
| 2 | `warming` | `ready` | the clone recipe (g) completes and the gitdir backlink checks out | D-lifecycle; D-leases |
| 3 | `ready` | `warming` | `upool refresh [--fetch]` moves idle ready slots to the current base (`origin/<base>` with `--fetch`) and re-warms them under the cold-import semaphore | D-commands; D-lifecycle |
| 4 | `ready` | `leased-unconfirmed` | `WorktreeCreate` hook, pooled branch: `upool take --no-create --holder <session_id> --timeout 20` | D-route |
| 5 | `ready` | `leased` | `upool take <name>` from Terminal, an explicit `take` by a subagent, `upool run` (see [Open questions](#open-questions) on whether these start confirmed) | D-commands; D-route |
| 6 | `leased-unconfirmed` | `leased` | `SessionStart` hook: `upool take --confirm "$cwd"`, matched by worktree path | D-env |
| 7 | `leased-unconfirmed` | `ready` | the lease is older than `confirm_timeout` (60 s): the app refused the session and no remove will come | D-leases; S§8 A.7 |
| 8 | `leased` | `needs-reset` | `WorktreeRemove` hook, `git status --porcelain` clean after reverting define-order noise | D-route |
| 9 | `leased` | `parked` | `WorktreeRemove` hook, any other tracked or untracked change | D-route; S§8 A.8 |
| 10 | `needs-reset` | `leased-unconfirmed` | `WorktreeCreate` hook, pooled branch, if it hands out `needs-reset` slots (Open question 5); the deferred reset (checkout base detached, `git clean -fd`) then runs inside transition 6's `take --confirm` | D-env; D-route |
| 11 | `leased` | `ready` | `upool release`, clean: `save_all` then `unity close <path>` for an Editor it opened, revert define-order noise, checkout base detached, `git clean -fd` | D-commands; D-lifecycle |
| 12 | `leased` | `parked` | `upool release`, dirty | D-commands |
| 13 | `leased` | `ready` | reclaim: holder PID dead **and** no live Editor in the ledger **and** tree clean | D-leases |
| 14 | `leased` | `parked` | reclaim: holder PID dead, tree dirty | D-leases |
| 15 | `ready` | removed | `upool prune --yes`: idle, merged, clean slots beyond `ready`; LRU to `max_disk`; `shrink_after` | D-commands; D-disk |
| 16 | `parked` | — | no automatic transition; `prune` lists it and a person resolves it | D-route |
| 17 | any | missing | the slot left the disk or `git worktree list`; `take` skips it, `doctor` reports it and it is recreated | D-route; S§10 |

`upool run <name> -- <cmd>` is transitions 5 then 11 or 12 around the command (D-commands).
`release` refuses a live session's slot without `--force` and honours `--if-lease-id` (D-commands).

Agent worktrees are not slots: the hook creates them, nothing releases them, and `prune` removes one
when it is clean and its parent process is gone or it is older than `agent_ttl` (D-route; D-commands;
S§9 F.3).

## (g) Clone recipe

Run from Terminal or the tool's own detached process, never inside the create hook. Every git call
sets `UNITY_SKIP_HOOKS` (D-lifecycle).

| Step | Shell | Source |
|---|---|---|
| 0 | Choose the seed: the main checkout with its Editor closed for the first slot, the freshest ready slot afterwards | D-lifecycle |
| 1 | Staleness check on the seed: newest mtime under `Assets/**/*.cs` against `Library/ScriptAssemblies`; `git -C <seed> rev-parse HEAD` and `git -C <seed> status --porcelain`; `unity cache key "<seed>"` only to catch an Editor or package change. If stale, refresh it: with an Editor running, `unity recompile`; without one, a batch open (about 16 s) | D-lifecycle; S§8 D.1, D.2 |
| 2 | Write the "acquisition incomplete" entry to `state.json` under the lock, then drop the lock | D-leases |
| 3 | `git -C <repo> worktree add --no-checkout --detach <repo>/.claude/worktrees/slot-N <commit>` (the spike passed `HEAD`) | S§2 TA; S§9 E.1 |
| 4 | For each top-level entry `<e>` of `<seed>` not in the exclusion list: `cp -cR <seed>/<e> <slot>/<e>`; `.claude` is copied without its `worktrees` child | S§2 TA; S§9 E.1; D-clone |
| 5 | `rm -f <slot>/Library/EditorInstance.json <slot>/Library/ProtocolInstance.json` and `rm -rf <slot>/Library/Pipeline` | D-lifecycle; S§2 T1.5–T1.6 |
| 6 | `git -C <slot> reset -q` to rebuild the index (7.3 to 7.7 s) | S§2 TA; S§9 E.1 |
| 7 | Verify `git -C <slot> status --porcelain` is empty, the slot's `.git` file points back into `<repo>/.git/worktrees/`, and the slot is in `git worktree list`; mark it `ready` | D-leases; D-route |

Exclusion list, exactly: `.git`, `Temp`, `Logs`, `obj`, `Builds`, `.claude/worktrees` (D-clone;
S§2 TA; S§9 E.1).

Measured: 25.9 s beside the repo (18.2 s copy, 7.7 s index) and 24.6 s in the managed directory
(17.3 s, 7.3 s); 47 MB and about 0 of disk; `git status` clean; mtimes preserved; 0 assemblies
recompiled on first open; the 21 relative symlinks under `.claude/skills` resolve inside the slot
(S§2 TA; S§9 E.1; S§10). `doctor` checks the preconditions: `.claude/worktrees` ignored
(`git check-ignore`), `git ls-files | grep -c '^Library/'` prints 0, APFS clone sharing is physical
(D-commands; D-lifecycle).

## (h) RAM budget

| Setting | Default | Source |
|---|---|---|
| `editor_ram_budget_gb` | physical RAM / 4 (16 at 64 GB) | D-commands; D-editors |
| `editor_ram_gb` | 2.5, per long-lived Editor | D-commands; D-editors |
| `helper_ram_gb` | 2, once any Editor is up | D-commands; D-editors |
| cold-import charge | 4.5 GB while a cold import runs (no setting named) | D-editors |
| `max_cold_imports` | 1 | D-commands; D-editors |
| `max_editors` | derived unless set; 3 at 64 GB | D-commands; D-editors |

Charge against the budget:

```text
charge = (helper_ram_gb if any Editor is in the ledger, else 0)
       + editor_ram_gb × (long-lived Editors)
       + 4.5 × (cold imports running)
admit an open when the new charge ≤ editor_ram_budget_gb and cold imports ≤ max_cold_imports

max_editors (when unset) = floor((editor_ram_budget_gb − helper_ram_gb − 4.5 × max_cold_imports) / editor_ram_gb)
                         = floor((16 − 2 − 4.5) / 2.5) = 3 at 64 GB
```

The `max_editors` formula is derived, not quoted[^4]. The gate is this ledger count, never `memory_pressure`, which read 92 to 93 % free under 12.5 GB of
Editor RSS (D-editors; S§6). Measured basis: warm batch Editors 1.43 GB each; idle GUI Editors 1.8
to 1.9 GB after 3 minutes; idle headless Editors 1.9 to 2.3 GB after 19.5 hours; a cold import peaks
at 4.2 to 4.5 GB; two concurrent cold imports, 12.5 GB total and 10 % slower, both exited 0 (S§2 T5,
T5.5; S§8 B.5, C.1).

Other defaults from the same paragraph: `ready = 1`, `max_slots = 3`, `shrink_after = 14d`,
`agent_ttl = 24h`; `confirm_timeout = 60s` (D-commands; D-leases).

## Open questions

Each question keeps its number wherever it is cited.

### Decide before Stage 2

These define M0's state types. M0 and M1 run one issue at a time, so a `decision-needed` issue for
any of them would stall the whole chain: answer each one here before Stage 2 (design.md,
Appendix A) starts.

- **4. `warming`.** Neither document names it. Is it a persisted state (the "acquisition incomplete"
  entry), and does `refresh`'s re-warm take a ready slot out of circulation while it runs?
- **5. `needs-reset`.** `take` lists `needs-reset` as a skip reason (D-leases), yet the
  `SessionStart` hook "runs the deferred reset if the slot is marked `needs-reset`" (D-env), which
  only happens if the create hook handed such a slot out. Does the pooled create branch accept
  `needs-reset` slots? If not, what resets them?
- **6. Leases outside the hook.** Do leases from a Terminal `take`, `upool run` or a subagent's
  explicit `take` start `confirmed: true`? If not, they would expire after `confirm_timeout` with no
  `SessionStart` to confirm them.
- **7. Schema keys.** The design gives no key names (except `lease_id` and `confirmed`), no timestamp
  format, no schema-version field and no derivation for `<repo-key>`. The placeholders in (e) need a
  decision before M0's state types.
- **10. Clone commit.** The spike cloned at the seed's `HEAD`; the design says "clone at base". If
  base differs from the seed's `HEAD`, step 6 leaves the slot dirty. Must the seed be at base, or
  does the recipe check base out after step 6?

### Other open questions

- **1. Unity CLI exit 7.** No capture shows it. What produces it, and what should upool do with it?
  `internal/output` defines no constant for 7 until this is answered.
- **2. Unrecorded exit codes.** The exits for `AMBIGUOUS_EDITOR`, `INVALID_COMMAND_ARGS`, the
  first-eval `COMMAND_FAILED` timeout, `build`'s "Not a Unity project" and `status` filtered to a
  project with no Editor (without `--until-ready`) were not recorded, and `AMBIGUOUS_EDITOR` exists
  only as a jq projection. The fake `unity` needs all of them; a new capture with
  `scripts/capture-unity.sh`, which records exit codes, should supply them.
- **3. upool's exit 1 and 6.** Is 1 the generic failure for every command or only `which`'s "not in
  a slot"? Does upool pass the Unity CLI's exit 6 through unchanged from its wrappers?
- **8. Saved scene list.** Where does `upool close` keep `Library/LastSceneManagerSetup.txt` between
  a headless open and its close?
- **9. Cold-import charge.** Is a cold-importing Editor charged 4.5 GB instead of its 2.5 GB or on
  top of it, is 4.5 GB a setting, and is the `max_editors` formula in (h) the intended one?
- **11. WorktreeCreate wait.** The pooled branch may wait up to 20 s for a slot. How long the desktop
  app waits for a create hook before failing was never measured.
- **12. `unity open` warning line.** Was the `Warning: …` line before the JSON on stdout or stderr? A
  parser that expects bare JSON on stdout breaks if it was stdout. `scripts/capture-unity.sh` keeps
  stdout and stderr apart, so a new capture answers this.
- **13. Pooled-repo match.** Does a pooled request require stdin `cwd` to equal the registered main
  checkout, or may it be a subdirectory? Every capture had `cwd` at the repository root.
- **14. Other `SessionStart` sources.** Only `source: "startup"` was captured. Should
  `take --confirm` behave differently on `resume`, `clear` or `compact`?
- **15. CLI remove and `SessionEnd`.** No `WorktreeRemove` or `SessionEnd` stdin exists for a
  logged-in CLI session, so those code paths have no fixture (D-unverified; S§8 A.9).
- **16. GoReleaser `brews`.** The design and the bootstrap ask for a `brews` section that writes
  `Formula/upool.rb`; GoReleaser 2.10 and later deprecate `brews` in favour of `homebrew_casks`.
  Keep the formula or move to a cask before the first release in M4? A cask is quarantined by
  macOS on install, and the binary is not signed. A quarantined binary in the create hook would
  likely fail every worktree session with `worktree_hook_failed`, so keeping the formula avoids
  this unless the binary is signed and notarized.

[^1]: The design (D-route) calls the desktop `session_id` "the app's own id" and the spike (S§2 TB)
    writes it as `local_<id>`. The captured stdin carries the bare UUID (`e8c6f3e6-…`); only
    `main.log` adds the `local_` prefix (compare `hooks/sibling-session1-create.json` with
    `evidence/logs/sibling-slot-main.log`). Per the spike's own captures, strip `local_` before comparing.
[^2]: The design (D-env) says the export "reached the Bash tool in both the CLI and the desktop app on
    2.1.295". The spike confirmed delivery only through the desktop app (S§2 T7.5; S§7); the
    standalone CLI run stopped at "Not logged in" before any Bash call (S§2 T7.4; S§8 A.9). The spike
    wins: delivery through a standalone CLI session is unverified.
[^3]: "warming" appears in neither document. It is mapped here to the design's "acquisition
    incomplete" entry (D-leases) and to `refresh`'s re-warm (D-commands); see Open question 4.
[^4]: The design gives the defaults and the result ("at 64 GB that admits three Editors with headroom
    for one cold import", D-editors) but not a formula; this is the closed form that reproduces it.
    The spike's figure is "3 at 64 GB is comfortable, 4 is plausible" (S§8 "What this changes
    (updated)"), which the formula respects. See Open question 9.
