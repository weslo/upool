# upool contracts

The external behaviour `upool` depends on, extracted from [design.md](design.md) and
[spike-report.md](spike-report.md). Every row cites its source. Where the two documents disagree,
the spike report wins and a footnote says so. Fixture names refer to files under
[`testdata/captures/`](../testdata/README.md), except those starting `evidence/`, which are under
[`docs/evidence/`](evidence/README.md). Nothing here is new design except the maintainer's answers
to open questions, cited A4 to A7, A10, A11, A13 to A15 and A17 to A26: they settle what both
documents leave open, and the tables follow them. Every other gap is listed under
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
| An | this document, the answer to open question n under [Open questions](#open-questions) (maintainer decision, 2026-10-18) |

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
| Process running the hook | parent `Claude.app/Contents/Helpers/disclaimer`, grandparent `Claude.app/Contents/MacOS/Claude`; under `~/Documents` only `ls -d` and `git` worked. A binary there runs, upool's included; a file there cannot be read, so a script there fails (A24). With desktop app 3.1623.0 the hook's parent was `Claude.app/Contents/MacOS/Claude` itself | the CLI | the bundled CLI binary (`…/claude.app/Contents/MacOS/claude`), full file access | S§2 TB; S§9 F.2–3; A24; `evidence/exec-probe/hook.txt` |
| `CLAUDE_ENV_FILE` | unset | not recorded | not recorded | S§7 (desktop probe log) |
| Fixtures | `hooks/desktop-session-create.json`, `-refused`, `-reuse`; `hooks/sibling-*-create.json`; `hooks/desktop-probe-create.json` | `hooks/cli-session-create.json` | `hooks/subagent-create.json`, `hooks/subagent-create-2.json` | testdata/README.md |

### Telling the kinds apart

| Kind | Distinguishing field | Test | Source |
|---|---|---|---|
| Subagent | `prompt_id` | present; corroborated by `scratchpad_dir` present and `name` matching `agent-<17 hex>` | S§9 F.3; S§9 "What this changes (updated)", subagent row; D-route |
| Desktop session | `transcript_path` | `""` and no `prompt_id` | S§9 "How to tell the requests apart" |
| CLI `--worktree` session | `transcript_path` | non-empty and no `prompt_id` | S§9 "How to tell the requests apart" |

These tests are for `WorktreeCreate`. A session's other requests can carry a `prompt_id` too: the
CLI's `WorktreeRemove` and `SessionEnd`, and `SessionStart` after `/compact`
(`hooks/cli-session-remove.json`, `hooks/cli-session-end.json`, `hooks/session-start-compact.json`;
#148), so a `prompt_id` marks a subagent on a create only.

The hook's three branches (D-route) key on subagent versus not, then on whether `cwd` is inside a
repository in `~/.upool/registry.json` (A13): subagent → `git worktree add --detach
<repo>/.claude/worktrees/<name>`; pooled session → `upool take --no-create --holder <session_id>
--timeout 20`, with the wait A11 sets; anything else → pass-through
`git -C <cwd> worktree add <cwd>/.claude/worktrees/<name>`.
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
| The hooks are registered per project, in its `.claude/settings.local.json` (exec form), by `init` (A26). The app runs a project-scope create hook and uses the path it prints, but skips the per-machine config overlay and auto-trust for the worktree: "hook came from a repo-committed settings tier … (provenance not vouched)". A user-scope hook clears that check, but runs in every repository | S§2 TB; S§8 A.2–3, A.5; A26; `evidence/logs/desktop-probe-main.log`; `evidence/logs/desktop-project-scope-main.log` |
| A hook script must live outside `~/Documents`: under it, `getcwd` and `bash` failed with `Operation not permitted`. A binary under it runs as the hook command, so upool registers itself, with no script (A24) | S§2 T7.6; S§7; A24; `evidence/exec-probe/helper.txt`; `evidence/logs/desktop-exec-probe-main.log` |
| Handing out a pre-made slot took `worktree=36ms` to `112ms`, against `4634ms` for a `git worktree add` inside the hook | S§8 A.5; S§9 E.5; `evidence/logs/sibling-slot-main.log`; `evidence/logs/desktop-probe-main.log` |
| A create hook runs for every repository whose settings register it, and every subagent there; there is no matcher. Registered per project, upool's run only in the projects `init` registers (A26) | S§9 "What this changes (updated)", blast radius row; D-route; A26 |
| The desktop app waited the whole 30 s for a create hook that slept that long, and used the path it printed (`worktree=30210ms`): its limit is above 30 s, so the pooled branch's wait, the smaller of 20 s and that limit less 5 s, is 20 s | A11; `hooks/desktop-create-timeout.txt` (desktop app 3.1623.0; #148) |

### WorktreeRemove fields

| Field | Desktop session | CLI `--worktree` session | Subagent | Source |
|---|---|---|---|---|
| `session_id` | same value as the matching create | the CLI session's UUID | never fires | S§8 A; S§9 E.6; `hooks/cli-session-remove.json` |
| `transcript_path` | `""` | set, keyed by the worktree path | never fires | S§8 A; `hooks/cli-session-remove.json` |
| `cwd` | main checkout | the **worktree**, not the main checkout | never fires | S§8 A; `hooks/cli-session-remove.json` |
| `scratchpad_dir`, `prompt_id` | absent | present | never fires | `hooks/desktop-session-remove.json`; `hooks/cli-session-remove.json` |
| `hook_event_name` | `"WorktreeRemove"` | `"WorktreeRemove"` | never fires | S§2 TB; `hooks/cli-session-remove.json` |
| `worktree_path` | the path the create hook printed | the worktree's path | never fires | S§2 TB; S§9 E.6; `hooks/cli-session-remove.json` |
| When it fires | about 1 s after the session is archived | at `/exit` of an interactive `claude --worktree` session, the worktree removed (Claude Code 2.1.304); never for `claude -p --worktree` | never: not when the subagent finishes, not when the parent session is archived | S§2 T7.5; S§8 A.6; S§9 F.3; #148 |
| Fixtures | `hooks/desktop-session-remove.json`, `-reuse`; `hooks/sibling-*-remove*.json`; `hooks/desktop-probe-remove.json` | `hooks/cli-session-remove.json` | none | testdata/README.md |

| Rule | Source |
|---|---|
| Exit 0 with the directory still present is accepted: the app logs "Removed hook-based worktree", deletes its registry entry and can hand the same path out again | S§8 A.6–A.7; S§9 E.6 |
| Archive gives no warning about uncommitted work; the remove hook saw `?? spike-reuse.txt` | S§8 A.8; `git/remove-status-untracked.txt` |
| The app's Archive button runs the remove hook for a dirty slot too. Archiving through the app's session tool does not: for a dirty worktree it logs `Archived session` and nothing more, runs no `WorktreeRemove` and keeps its record of the worktree, which refuses the path to later sessions ("already in use"), even after the session is deleted, until the session is resumed and archived clean (A28) | A28; `evidence/logs/desktop-dirty-archive-main.log` |
| `git -C <slot> status --porcelain` works from the disclaimed helper; git writes (`checkout`, `clean`) and `unity` calls from it are untested | S§8 A, `~/Documents` row; D-unverified |
| A pass-through worktree, in a repository upool does not manage, is removed with `git -C <cwd> worktree remove <worktree_path>` when clean as (f) defines it, and kept, with a line on stderr, when dirty; the hook exits 0 either way | A22 |

## (b) SessionStart and `CLAUDE_ENV_FILE`

### SessionStart fields

| Field | Desktop, worktree session | Desktop, no worktree | CLI `-p --worktree` | Source |
|---|---|---|---|---|
| `session_id` | CLI session UUID; differs from the create/remove `session_id` | CLI session UUID | same value as the create's | S§8 "A, session ids"; S§5 |
| `transcript_path` | keyed by the slot path (`…/${repo_key}--claude-worktrees-slot-1/…`) | keyed by the repo path | keyed by the worktree path | S§5; S§9 E, added memory row |
| `cwd` | the slot | main checkout | the worktree | S§2 TB; S§5 |
| `scratchpad_dir` | present | present | absent | S§5 (both captures) |
| `hook_event_name` | `"SessionStart"` | `"SessionStart"` | `"SessionStart"` | S§5 |
| `source` | `"startup"`; also `"resume"` and `"compact"`, below | `"startup"` | `"startup"`; interactively, also `"clear"`, below | S§5; [SessionStart sources](#sessionstart-sources) |
| Process | the CLI process; `getcwd` works under `~/Documents` | same | same | S§2 TB |
| Process cwd | the **main checkout**, even when stdin `cwd` is the slot: read stdin `cwd`, never `$PWD` | main checkout | not recorded | S§2 TB; S§7 item 6 |
| Fixtures | `hooks/session-start.json`, `hooks/session-start-reuse.json`, `hooks/sibling-*-start.json`, `hooks/desktop-probe-session-start.json` | `hooks/desktop-no-worktree-session-start.json` | `hooks/cli-session-start.json` | testdata/README.md |

### SessionStart sources

Captured for A14 (#148; desktop app 3.1623.0, Claude Code 2.1.304). The CLI process is told by the
hook's parent PID.

| `source` | When | CLI process | Fixture |
|---|---|---|---|
| `"startup"` | a session starts | a new one | `hooks/session-start.json` and the others above |
| `"resume"` | a desktop worktree session opened again after the app was quit | a **new** one: the parent PID differs from `startup`'s, so the lease needs its holder PID again (A14) | `hooks/session-start-resume.json` |
| `"compact"` | `/compact` in a desktop worktree session | the same one; the stdin also carries `prompt_id`, `model` and `session_title` | `hooks/session-start-compact.json` |
| `"clear"` | `/clear` in an interactive `claude --worktree` session | the same one, with a new `session_id`; the old session's `SessionEnd` says `reason: "clear"` | `hooks/session-start-clear.json` |

In a desktop session without a worktree, `/clear` sent no `"clear"`: it started a new CLI session
whose `SessionStart` said `"startup"` (testdata/README.md, `session-start-clear.json`). Whether a
desktop worktree session's `/clear` does the same was not tried.

### `CLAUDE_ENV_FILE`

| Rule | Source |
|---|---|
| Set for `SessionStart` hooks to `~/.claude/session-env/<SessionStart session_id>/sessionstart-hook-0.sh` | S§2 T7.5 |
| The hook appends shell lines of the form `export NAME=value`; they reach the session's Bash tool in the desktop app, with and without a worktree[^2] | S§2 T7.5; S§7; S§8 A.5 (`UNITY_PROJECT_PATH=…/ta` seen in the reply) |
| Unset for `WorktreeCreate` | S§7 (desktop probe log) |
| upool's `SessionStart` hook runs `upool take --confirm "$cwd"`, which confirms the lease by path, re-copies `CLAUDE.md`, `AGENTS.md`, `.claude/` minus `worktrees` and `.agents/` from the main checkout, runs the deferred reset if the slot is marked `needs-reset`, records its own parent (the session's CLI process) as the lease's holder PID, appends `export UNITY_PROJECT_PATH=<slot>`, and spawns a detached `upool refresh`. That is for `source: "startup"`; other sources do less (A14) | D-env; D-lifecycle; A6; A14 |
| Project-level hooks for a slot session come from the **main checkout's** `.claude/settings*.json` and run with the main checkout as cwd; the slot's own copies never fired in five sessions | S§8 A.5; S§9 E, added hook source row |
| upool registers no `SessionEnd` hook (A25). At `/exit` of an interactive `claude --worktree` session it says `reason: "prompt_input_exit"`, with `cwd` the **main checkout** though the session ran in the worktree, and carries `prompt_id` and `scratchpad_dir`; `/clear` ends a session with `reason: "clear"`, and quitting the desktop app ended each of its sessions with `reason: "other"`. `CwdChanged` was not exercised | D-env; D-unverified; A15; A25; `hooks/cli-session-end.json`; testdata/README.md (#148) |

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
| `projects verify` | not tested with the variable | positional: `unity projects verify --strict <slot>` reported that slot as its `project` | #86; `unity/projects-verify-pass.json`, `unity/projects-verify-fail.json` |

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
| `unity open` prints `Warning: "<path>" was not found in the Hub project registry, opening as a file path.` on **stderr**, and repeats it in `warnings`; stdout is the bare envelope. The spike's recording put the line before the JSON because it caught both streams together (A12) | `unity/open-gui-streams.stderr.txt`, `unity/open-headless.stderr.txt`; `unity/open-gui.raw.txt` |
| `vcs git worktree` JSON redacts the home directory to `/Users/<user>`, so its paths cannot be used verbatim (upool never calls it); `projects verify` does the same | S§5; `unity/projects-verify-pass.json` |
| Fixtures write the directory a command ran in as `${cwd}`; the spike ran these from `/tmp`, which the CLI reports as `/private/tmp` | `testdata/README.md` |

### Codes upool must handle

| Exit | `errors[].code` | Message (captured) | When | Source | Fixture |
|---|---|---|---|---|---|
| 0 | — | — | success | S§2; S§8 | `unity/status-*.json`, `close-*.json`, … |
| 6 | `STATUS_NO_INSTANCES` | No Unity Editor instances found with the Pipeline package installed. Open Unity and install the Pipeline package, or run unity pipeline install. | `status --until-ready --timeout N` after waiting the **whole** timeout (15.1 s for 15) for a non-existent path or a project with no Editor | S§2 T6.5; D-editors | `unity/status-until-ready-no-instances.json`, `evidence/unity-projections/status-until-ready-closed-project.json` |
| 6 | `STATUS_NO_INSTANCES` | same | `status` filtered by the variable or `--project-path` to a project with no Editor | S§2 T6.4 | `unity/status-no-instances.json` (A2); `evidence/unity-projections/status-env-no-instances.json`, `status-project-path-no-instances.json` |
| 6 | `COMMAND_FAILED` | No Pipeline instance found for project: `<path>`. Make sure Unity Editor is running with the Pipeline package installed. | `unity command` with the variable naming a project that has no Editor | S§2 T6.4 (exit code from the spike's `commands.log`) | `unity/command-eval-no-pipeline-instance.json`, `evidence/unity-projections/command-list-no-pipeline-instance.json` |
| 6 | `COMMAND_FAILED` | Not a Unity project (ProjectVersion.txt not found at /private/tmp). | `test` or `run` with no positional path, outside a project | S§2 T6.6; S§8 B.3 | `unity/test-not-a-unity-project.json`, `unity/run-not-a-unity-project.json` |
| 6 | `COMMAND_FAILED` | same | `build` with no positional path | S§8 B.3 | `unity/build-not-a-unity-project.json` (A2); `evidence/unity-projections/build-not-a-unity-project.json` |
| not recorded | `COMMAND_FAILED` | Pipeline server returned 400 Bad Request: Internal Server Error. Main thread operation timed out after 5000ms | first `eval` on a freshly opened GUI Editor; **the code ran anyway**, so re-query side effects instead of retrying | S§8 C.3; D-editors | `unity/command-eval-main-thread-timeout.json`; it did not recur in #86, where the first `eval` answered at once (`unity/command-eval-first-call.json`, exit 0), so its exit stays unrecorded and upool reads its code (A2) |
| 6 | `AMBIGUOUS_EDITOR` | Multiple Unity Editors are running with Pipeline servers: … Pass --project-path \<path\> with one of the project paths listed above to choose one, or run the command from inside one of those project directories. | 2 or more Editors, cwd outside them, variable unset | S§8 B.2 | `evidence/unity-projections/command-eval-ambiguous-editor.json`; `unity/command-eval-ambiguous-editor.json` (A2) |
| 2 | `INVALID_COMMAND_ARGS` | error: unknown option '--project-path' | `cache key --project-path` | S§8 D.1 | `unity/cache-key-project-path-invalid.json`; `unity/cache-key-project-path-rejected.json` (A2) |
| 7 | — | — | **never observed** in any capture | — | none; a failure, upool exits 1 (A1) |

### upool's own exit codes

The design says only that "exit codes and the JSON envelope mirror the Unity CLI's own" and that
`which` exits 1 outside a slot (D-commands). These are the codes it pins down; `internal/output`
defines exactly these:

| Code | Constant | Meaning | Source |
|---|---|---|---|
| 0 | `ExitOK` | success | D-commands |
| 1 | `ExitFailure` | the command did not succeed, for every command; `upool which` returns it when cwd is not inside a slot; a wrapped Unity CLI call's exit maps to it unless it is 6 (A1; A3) | D-commands, command table; A1; A3 |
| 6 | `ExitNoTarget` | mirrors the Unity CLI's exit 6: no Editor instance, or no Unity project, at the resolved target, including a wrapped call that exits 6 (A3) | D-commands; S§2 T6.5, T6.6; A3 |

The hook subcommands follow the app's contract rather than this table: `worktree-remove` always
exits 0, and `worktree-create` exits non-zero only to abort the session (D-route).

## (e) State schemas

All three files live under `~/.upool/`, never under a TCC-protected folder, because the create hook
reads them from the disclaimed helper (D-commands). Each is one JSON document rewritten atomically
under one OS file lock, `~/.upool/lock`, shared by all three and held only across the
read-modify-write, never across a 26 s clone or a 235 s import (D-commands).

The keys are the snake_case names below, exactly, and the types are JSON types (A7). Timestamps are
RFC 3339 strings in UTC, as Go encodes `time.Time`; a process start time (`holder_pid_start`,
`owner_pid_start`) is the one `internal/proc` reads (sysctl, microseconds) and is compared at that
precision, never against another source such as `ps -o lstart` (A7). Each file has a top-level
`schema_version`, `1` for the format described here: a binary that reads a higher version refuses
with exit 1 and a message to upgrade upool, rather than rewriting the file and dropping fields it
does not know, and one that reads a lower version migrates it on its next write (A7).

### `~/.upool/pools/<repo-key>/state.json`

Pool:

| Field | Type | Meaning | Written by | Source |
|---|---|---|---|---|
| `schema_version` | integer | `1` | every writer | A7 |
| `repo` | string, absolute path | the main checkout | `init` | D-commands |
| `base` | string, git ref | the ref slots are cloned at and reset to | `init`: from flag, `UPOOL_*`, `.upool.toml` or user config; when none sets it, origin's default branch (`git symbolic-ref --short refs/remotes/origin/HEAD` without its `origin/` prefix), else the current branch, which `init` prints and writes into `.upool.toml` | D-commands; D-lifecycle; A17 |
| `slots` | array of slot | the pool's slots | see below | D-commands |
| `agent_worktrees` | array of agent worktree | subagent worktrees made by the hook | hook (subagent branch), `prune` | D-route |
| `refresh_job` | object or null: `pid` (integer) and `pid_start` (timestamp), both always set | the one detached top-up job, announced in `status`: its PID and that process's start time, so a reused PID is not mistaken for it | `refresh`, which the session-start hook spawns after a confirmed startup: records itself as it starts, unless a live job is recorded, and clears itself as it ends (#79) | D-build; D-env; A7 |

Slot:

| Field | Type | Meaning | Written by | Source |
|---|---|---|---|---|
| `name` | string | `slot-N` | `init`, `take` (create), `refresh` | D-commands |
| `path` | string, absolute path | `<repo>/.claude/worktrees/slot-N` | same | D-commands |
| `state` | string, one of the states in (f) | lifecycle state; `creating` is the "acquisition incomplete" entry written before `git worktree add` | every transition in (f) | D-leases; D-route; A4 |
| `owner_pid` | integer or null | the process cloning the slot (`creating`) or re-warming it (`warming`); null in every other state. A dead owner starts recovery, (f) transitions 21 and 22 | clone (`init`, `take`, `refresh`), `refresh` | A4 |
| `owner_pid_start` | timestamp or null | that process's start time, so a reused PID is not mistaken for the owner | same | A4 |
| `seed_head` | string, commit SHA | the seed source's `git rev-parse HEAD` at clone time: the commit the cloned Library matches. The slot is then moved to base ((g) step 7), so its own HEAD may differ | clone (`init`, `take`, `refresh`) | D-lifecycle; S§8 "What this changes (updated)", staleness row; A10 |
| `seed_dirty` | bool | whether the seed source had uncommitted changes, which (g) step 7 reverts in the slot | clone | same |
| `reimport_pending` | bool | a checkout upool made in the slot changed `unity cache key`, so the next open is a large reimport | set by every checkout upool makes ((g) step 7, `release`, the deferred reset, `refresh`); cleared by the next `upool open`, which charges that open as a cold import | A20 |
| `last_used` | timestamp | for LRU removal under `max_disk` and for `shrink_after` | `release`, hook (remove) | D-disk; D-commands |
| `lease` | lease or null | current lease | `take`, `take --confirm`, `release`, hooks, reclaim | D-leases |

Lease:

| Field | Type | Meaning | Written by | Source |
|---|---|---|---|---|
| `lease_id` | string, random | identity for `release --if-lease-id` | `take` | D-commands; D-leases |
| `holder` | string | `--holder ID`; for the desktop hook, the create stdin `session_id` | `take` | D-route |
| `worktree_path` | string, absolute path | correlation key for `--confirm`, remove and release | `take` | D-route; D-leases |
| `holder_pid` | integer or null | the process whose life the lease follows, for dead-PID reclaim: `upool run`'s own PID; for a create-hook lease, null until `take --confirm` sets it to the `SessionStart` hook's parent, the session's CLI process (unverified that no shell sits between them, as for `parent_pid` below); null for a bare `take`, which is never reclaimed automatically | `take`, `run`, `take --confirm` | D-leases; A6 |
| `holder_pid_start` | timestamp or null | that process's start time, so a reused PID is not mistaken for the holder | same | D-leases; A6 |
| `acquired_at` | timestamp | when the lease was taken; drives `confirm_timeout`, and for a lease with no holder PID, `doctor`'s warning once it is older than `agent_ttl` | `take` | D-commands; D-leases; A6 |
| `confirmed` | bool | `false` only for a lease the create hook takes, set inside `internal/hooks` and never by a CLI flag; `true` after `take --confirm`, and from the start for every other `take`, including `run`'s | `take`, `take --confirm` | D-route; D-env; A6 |
| `reset_pending` | bool | `true` only for a create-hook lease on a slot handed out from `needs-reset` (transition 10), whose deferred reset has not run: `take --confirm` runs the reset and clears it, and an expiry before then returns the slot to `needs-reset`, not `ready` (transition 7) | hook (create), `take --confirm` | A5; A23 |
| `lane` | string, `script` or `scene` | default `script`; at most one `scene` lease per project | `take --lane` | D-lanes |
| `name_hint` | string | the create stdin `name`; never a key | hook (create) | D-route |

A lease is this entry alone: no lease takes a `git worktree lock`, since the desktop app refuses to
archive a locked worktree and runs no `WorktreeRemove` for it (A27). A lease has no name either:
`take <name>` names a branch, and `path`, `release` and `run` find a slot by the branch git reports
checked out in it (A18).

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
| `schema_version` | integer | `1` | `init` | A7 |
| `repos` | array of repo | every pooled repository on the machine | `init` | D-commands |
| `repos[].path` | string, absolute path | main checkout; the create hook matches stdin `cwd` against it | `init`, run from Terminal | D-commands; D-route |
| `repos[].key` | string | the `<repo-key>` naming `~/.upool/pools/<repo-key>/` | `init` | D-commands; A7 |

`<repo-key>` is the last element of the canonical path (`internal/paths`), a `-`, and the first 8
hex digits of the SHA-256 of the canonical path, for example `down-3f9a1c2e` (A7). This file is the
source of truth: lookups go by path, and the derivation matters only so that `init` finds an
existing pool again after the registry is lost. It is not the `${repo_key}` of
[testdata/README.md](../testdata/README.md), which is Claude Code's project key for the same path.

The hook reads this file instead of `.upool.toml` because `head` on a file under `~/Documents` failed
from the disclaimed helper (D-commands; S§2 TB).

### `~/.upool/editors.json` (the Editor ledger)

| Field | Type | Meaning | Written by | Source |
|---|---|---|---|---|
| `schema_version` | integer | `1` | every writer | A7 |
| `editors` | array of editor | every Editor `upool` is charging for | `open`, `close`, `release`, `refresh`, `doctor --fix` | D-commands; D-editors |
| `editors[].project` | string, absolute path | the slot the Editor has open | `open` | D-editors |
| `editors[].pid` | integer | Editor PID; a dead PID is reclaimed | `open`; removed by `close`, `release`, `doctor --fix` | D-editors; D-leases |
| `editors[].mode` | string, `headless` or `gui` | how `open` launched it | `open` | D-commands |
| `editors[].opened_by_upool` | bool | `release` closes only Editors the tool opened | `open` | D-commands |
| `editors[].cold_import` | bool | holds a cold-import slot (`max_cold_imports`) while true | `open`, `refresh` | D-editors |
| `editors[].charge_gb` | number | 2.5 when long-lived, 4.5 while a cold import runs | `open`, `refresh` | D-editors |

The helper charge (2 GB once any Editor is up) is computed from the list, not stored (D-editors).

## (f) Slot lifecycle

A slot is **clean** when `git status --porcelain` prints nothing once define-order noise is
reverted, **and** some ref contains its HEAD (`git for-each-ref --contains HEAD` prints at least one
line). Anything else is **dirty**. The second test is there because the app creates no branch for a
hook-made worktree and slots are handed out detached (S§9 E.5): a session's commits pass
`git status`, and a reset to base would orphan them (A5).

| State | Meaning | `take` skip reason | Source |
|---|---|---|---|
| `creating` | being cloned: the "acquisition incomplete" entry, written before `git worktree add` with the cloning process in `owner_pid`[^3] | `creating` | D-leases; A4 |
| `warming` | a ready slot out of circulation, with the process holding it in `owner_pid`, while `refresh` moves it to base and re-warms it, or while a clone copies from it as its seed (A19); it is `ready` again afterwards[^3] | `warming` | D-commands; A4; A19 |
| `ready` | detached at base, clean, unleased | — (handed out first) | D-lifecycle |
| `leased-unconfirmed` | leased by the create hook, `confirmed: false` | `unconfirmed` | D-route; D-leases |
| `leased` | leased and confirmed | `leased` | D-env; D-leases |
| `parked` | dirty after a remove or release: unleased, not ready, never reset, surfaced by `prune` | `parked` | D-route |
| `needs-reset` | clean after a remove; the reset is deferred to the confirm step of its next lease, or to `refresh` | — (handed out only when no slot is `ready`) | D-route; D-env; A5 |

`take` also skips slots for `editor-running` and `missing-from-worktree-list`, which are conditions,
not states (D-leases). It hands out a `ready` slot if there is one and a `needs-reset` slot
otherwise, and runs that slot's reset (checkout base detached, `git clean -fd`) in the confirm
step: for a create-hook lease, in `SessionStart`'s `take --confirm`, which has full file access; for
any other `take`, before it prints the path (A5). `refresh` resets `needs-reset` slots before it
clones anything, and counts `creating` and `warming` slots as ready when it tops the pool up to
`ready` (A4; A5).

| # | From | To | Caused by | Source |
|---|---|---|---|---|
| 1 | — | `creating` | `upool init` (first slot, from the main checkout with its Editor closed); `upool take` when no slot is `ready` or `needs-reset` and the pool is below `max_slots` (not with `--no-create`); `upool refresh` topping the pool up to `ready`, spawned detached by the `SessionStart` hook. After the first slot, only when a seed qualifies ((g) step 0) | D-commands; D-lifecycle; D-env; A4; A19 |
| 2 | `creating` | `ready` | the clone recipe (g) completes and its step 8 checks pass | D-lifecycle; D-leases; A4 |
| 3 | `ready` | `warming` | `upool refresh [--fetch]` moves an idle ready slot to the current base (`origin/<base>` with `--fetch`) and re-warms it under the cold-import semaphore | D-commands; D-lifecycle; A4 |
| 4 | `ready` | `leased-unconfirmed` | `WorktreeCreate` hook, pooled branch: `upool take --no-create --holder <session_id> --timeout 20` | D-route |
| 5 | `ready` | `leased` | `upool take <name>` from Terminal, an explicit `take` by a subagent, `upool run`; `take` checks out the branch `<name>`, creating it from `--base` (default `base`) when it does not exist, and the lease starts confirmed | D-commands; D-route; A6; A18 |
| 6 | `leased-unconfirmed` | `leased` | `SessionStart` hook: `upool take --confirm "$cwd"`, matched by worktree path; it records the holder PID | D-env; A6 |
| 7 | `leased-unconfirmed` | `ready`, or `needs-reset` when the lease's `reset_pending` is set | the lease is older than `confirm_timeout` (60 s): the app refused the session and no remove will come. `take`, `status` and `doctor` end such a lease whenever they read the pool; the slot goes back to the state it was handed out from, since a slot that came through transition 10 never had its deferred reset | D-leases; S§8 A.7; A23 |
| 8 | `leased` | `needs-reset` | `WorktreeRemove` hook, the slot clean | D-route; A5 |
| 9 | `leased` | `parked` | `WorktreeRemove` hook, the slot dirty | D-route; S§8 A.8; A5 |
| 10 | `needs-reset` | `leased-unconfirmed` | `WorktreeCreate` hook, pooled branch, when no slot is `ready`; the deferred reset then runs inside transition 6's `take --confirm` | D-env; D-route; A5 |
| 11 | `leased` | `ready` | `upool release`, clean: `save_all` then `unity close <path>` for an Editor it opened, revert define-order noise, checkout base detached, `git clean -fd` | D-commands; D-lifecycle; A5 |
| 12 | `leased` | `parked` | `upool release`, dirty | D-commands; A5 |
| 13 | `leased` | `ready` | reclaim: the lease has a holder PID **and** it is dead **and** no live Editor in the ledger **and** the slot clean. A lease with no holder PID (a bare `take`) is never reclaimed: it ends on `release`, or a person's `release --force`, and `doctor` warns once it is older than `agent_ttl` | D-leases; A6 |
| 14 | `leased` | `parked` | reclaim: the holder PID dead, the slot dirty | D-leases |
| 15 | `ready` | removed | `upool prune --yes`: idle, merged, clean slots beyond `ready`; LRU to `max_disk`; `shrink_after` | D-commands; D-disk |
| 16 | `parked` | — | no automatic transition; `prune` lists it and a person resolves it | D-route |
| 17 | any | missing | the slot left the disk or `git worktree list`; `take` skips it, `doctor` reports it and it is recreated | D-route; S§10 |
| 18 | `warming` | `ready` | `refresh` finishes the move to base and the re-warm, or the clone that holds the slot as its seed finishes copying it, and the slot is clean | D-commands; A4; A19 |
| 19 | `needs-reset` | `leased` | `upool take <name>` from Terminal, an explicit `take` by a subagent, `upool run`, when no slot is `ready`; the reset runs, then `take` checks out the branch `<name>` as in transition 5, before the path is printed, and the lease starts confirmed | A5; A6; A18 |
| 20 | `needs-reset` | `ready` | `upool refresh`, before it clones anything: checkout base detached, `git clean -fd` | A5 |
| 21 | `creating` | removed | the owner PID is dead: `doctor --fix`, or the next `take` or `refresh`, removes the slot's worktree registration if `git worktree add` got that far (`git worktree remove --force`), then its directory and its entry | A4 |
| 22 | `warming` | `ready` or `parked` | the owner PID is dead and the ledger shows no live Editor in the slot: the slot is checked again; clean → `ready`, and a later `refresh` moves it to base; dirty → `parked` | A4 |
| 23 | `ready` | `warming` | a clone chooses the slot as its seed ((g) step 0) and holds it while step 4 copies it | A19 |

`upool run <name> -- <cmd>` is transition 5 or 19, then 11 or 12, around the command (D-commands).
`take <name>` fails, naming the worktree, when git already has the branch checked out in another
worktree, the main checkout included (A18).

Every checkout upool makes in a slot ((g) step 7, the resets in transitions 10, 11, 19 and 20, and
`refresh`'s move in transition 3) compares `unity cache key "<slot>"` before and after it. A change
means the next open is a large reimport: upool reports it and sets the slot's `reimport_pending`,
and the next `upool open` charges that open as a cold import and clears it (A20).
`release` refuses a live session's slot without `--force` and honours `--if-lease-id` (D-commands).

Agent worktrees are not slots: the hook creates them, nothing releases them, and `prune` removes one
when it is clean and its parent process is gone or it is older than `agent_ttl` (D-route; D-commands;
S§9 F.3).

## (g) Clone recipe

Run from Terminal or the tool's own detached process, never inside the create hook. Every git call
sets `UNITY_SKIP_HOOKS` (D-lifecycle).

| Step | Shell | Source |
|---|---|---|
| 0 | Choose the seed. `init`'s first slot seeds from the main checkout, with its Editor closed. Any later clone takes the first of these that has no live Editor (ledger or instance files) and passes step 1: the freshest ready slot; a `needs-reset` slot, after its reset; the main checkout. A slot chosen as the seed is held as `warming` until step 4 has copied it ((f) transition 23). If none qualifies, nothing is cloned: `take` waits or fails as for a full pool, and the top-up exits, each saying why | D-lifecycle; A19 |
| 1 | Staleness check on the seed: newest mtime under `Assets/**/*.cs` against the Editor's last compile, the newer of `Library/ScriptAssemblies` and the newest `.dll` in the build cache's Editor DAG, `Library/Bee/artifacts/<hash>E….dag/` (the Editor copies an assembly into `ScriptAssemblies` only when its content changed; player DAGs such as `<hash>P.dag` do not count); a missing `ScriptAssemblies` is stale. `git -C <seed> rev-parse HEAD` and `git -C <seed> status --porcelain`. If stale, refresh it with a batch open (about 16 s); a seed never has a live Editor (step 0), so the design's `unity recompile` case does not arise. `unity cache key` is compared across checkouts instead (step 7; (f)) | D-lifecycle; S§8 D.1, D.2; A19; A20; #144 |
| 2 | Write the `creating` entry, with this process in `owner_pid`, to `state.json` under the lock, then drop the lock | D-leases; A4 |
| 3 | `git -C <repo> worktree add --no-checkout --detach <repo>/.claude/worktrees/slot-N <seed-head>`, where `<seed-head>` is `git -C <seed> rev-parse HEAD`, as the spike measured | S§2 TA; S§9 E.1; A10 |
| 4 | For each top-level entry `<e>` of `<seed>` not in the exclusion list: `cp -cR <seed>/<e> <slot>/<e>`; `.claude` is copied without its `worktrees` child | S§2 TA; S§9 E.1; D-clone |
| 5 | `rm -f <slot>/Library/EditorInstance.json <slot>/Library/ProtocolInstance.json` and `rm -rf <slot>/Library/Pipeline` | D-lifecycle; S§2 T1.5–T1.6 |
| 6 | `git -C <slot> reset -q` to rebuild the index (7.3 to 7.7 s) | S§2 TA; S§9 E.1 |
| 7 | Move the slot to base. If the seed was dirty, `git -C <slot> checkout -- .` and `git -C <slot> clean -fd`, which keeps gitignored files; then, if HEAD is not base, `git -C <slot> checkout --detach <base>`. The first open imports only the difference between `<seed-head>` and base; a seed already clean at base, as every ready slot is, makes this step do nothing. Compare `unity cache key "<slot>"` before and after the checkout; a change sets `reimport_pending` | A10; A20 |
| 8 | Verify `git -C <slot> status --porcelain` is empty, HEAD is base, the slot's `.git` file points back into `<repo>/.git/worktrees/`, and the slot is in `git worktree list`; mark it `ready` | D-leases; D-route; A10 |

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
| cold-import charge | 4.5 GB, instead of `editor_ram_gb`, while the Editor's cold import runs; a constant, not a setting (A9) | D-editors; A9 |
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

The `max_editors` formula is derived, not quoted[^4], and A9 keeps it. The gate is this ledger count, never `memory_pressure`, which read 92 to 93 % free under 12.5 GB of
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
any of them would stall the whole chain: each had to be answered here before Stage 2 (design.md,
Appendix A) could start. All five are answered (maintainer decision, 2026-10-18), and (e), (f) and
(g) follow the answers.

- **4. `warming`.** Neither document names it. Is it a persisted state (the "acquisition incomplete"
  entry), and does `refresh`'s re-warm take a ready slot out of circulation while it runs?

  **A4.** Yes to both, as two persisted states. `creating` is the "acquisition incomplete" entry:
  written before `git worktree add` with the cloning process in `owner_pid`, so that while the lock
  is dropped no other process hands the slot out, picks the same `slot-N`, or leaves it out of the
  `max_slots` count. `warming` is a ready slot that `refresh` is moving to base and re-warming. It
  is out of circulation until it is `ready` again, because a session could otherwise land in a slot
  mid-checkout, or in one whose batch Editor holds `Temp/UnityLockfile`. `take` skips both, and the
  top-up counts both as ready. A dead owner is recovered by state: a `creating` slot is removed, and
  a `warming` slot is checked again and goes back to `ready` if clean or to `parked` if not ((f),
  transitions 1–3, 18, 21 and 22).
- **5. `needs-reset`.** `take` lists `needs-reset` as a skip reason (D-leases), yet the
  `SessionStart` hook "runs the deferred reset if the slot is marked `needs-reset`" (D-env), which
  only happens if the create hook handed such a slot out. Does the pooled create branch accept
  `needs-reset` slots? If not, what resets them?

  **A5.** Yes, when no slot is `ready`: every `take` hands out `ready` slots first and `needs-reset`
  ones after, so `needs-reset` is not a skip reason. The reset runs in the confirm step: for a
  create-hook lease, in `SessionStart`'s `take --confirm`; for any other `take`, before the path is
  printed. `refresh` also resets `needs-reset` slots to `ready`, before it clones anything. And a
  clean slot now also needs a HEAD that some ref contains, because hook-made slots run detached
  (S§9 E.5) and a reset would otherwise orphan a session's commits; such a slot is parked ((f),
  the definition of clean and transitions 8–12, 19 and 20).
- **6. Leases outside the hook.** Do leases from a Terminal `take`, `upool run` or a subagent's
  explicit `take` start `confirmed: true`? If not, they would expire after `confirm_timeout` with no
  `SessionStart` to confirm them.

  **A6.** Yes. Only the create hook's lease starts with `confirmed: false`, set inside
  `internal/hooks` and never by a CLI flag. Dead-PID reclaim needs a holder PID that lives as long
  as the lease: `upool run` records its own; a create-hook lease gets the `SessionStart` hook's
  parent, the session's CLI process, at confirm; a bare `take` records none, because its own process
  exits at once and its parent, from a Claude Code Bash tool call, is probably a shell that exits
  after each command (unverified). A lease with no holder PID is never reclaimed automatically: it
  ends on `release`, or a person's `release --force`; `status` shows its age and `doctor` warns once
  it is older than `agent_ttl` ((e) Lease; (f) transitions 5, 6, 13 and 19).
- **7. Schema keys.** The design gives no key names (except `lease_id` and `confirmed`), no timestamp
  format, no schema-version field and no derivation for `<repo-key>`. The placeholders in (e) need a
  decision before M0's state types.

  **A7.** The keys are the snake_case names in (e), exactly. Timestamps are RFC 3339 strings in
  UTC; process start times come from `internal/proc` and are compared at its precision. Each file
  has a top-level `schema_version`, `1` for now: a newer version is refused, an older one migrated
  on the next write. `<repo-key>` is the canonical path's last element, a `-`, and the first 8 hex
  digits of the SHA-256 of the canonical path (`down-3f9a1c2e`); the registry stays the source of
  truth ((e)). The `refresh_job` object, which (e) first gave only as "PID, start time", has the
  keys `pid` and `pid_start`, both always set while the object is present
  ([#38](https://github.com/weslo/upool/issues/38)).
- **10. Clone commit.** The spike cloned at the seed's `HEAD`; the design says "clone at base". If
  base differs from the seed's `HEAD`, step 6 leaves the slot dirty. Must the seed be at base, or
  does the recipe check base out after step 6?

  **A10.** The recipe checks base out. It clones at the seed's HEAD, as the spike measured, then
  reverts anything a dirty seed left in the slot and runs `git checkout --detach <base>` before the
  final checks ((g) step 7). The first open imports only the difference between the seed's HEAD
  and base, the same delta-only cost `refresh --fetch` accepts, and `seed_head` records the commit
  the Library matches. A seed already clean at base, as every ready slot is, costs nothing extra.

### Decided for M1

Stage 2 filed these as `decision-needed` issues because each blocks the M1 chain. All five are
answered (maintainer decision, 2026-10-18), and (e), (f) and (g) follow the answers.

- **17. The default for `base`**
  ([#22](https://github.com/weslo/upool/issues/22)). (e) has `init` take `base` from flag,
  `UPOOL_*`, `.upool.toml` or user config, and design.md gives every other setting a default but
  not this one; `init` has no `--base` flag. What is `base` when nothing sets it?

  **A17.** origin's default branch: `git symbolic-ref --short refs/remotes/origin/HEAD`, without its
  `origin/` prefix. When that ref is missing (a repository created locally and pushed later), the
  current branch. `init` prints which it chose and writes it into `.upool.toml`, so the value is
  visible, editable and shared. A local base branch behind `origin/<base>` makes new slots start
  behind it until `refresh --fetch` ((e) `base`).
- **18. What `take <name>` names**
  ([#23](https://github.com/weslo/upool/issues/23)). design.md gives `take <name>`, `path <name>`,
  `release [path|name]` and `run <name>`, and says take checks out "the requested branch", but (e)
  has no lease name and the create hook's take passes none. What does `<name>` name?

  **A18.** A branch. `take` checks `<name>` out in the slot, creating it from `--base` (default
  `base`) when it does not exist; `path`, `release` and `run` find the slot whose HEAD is that
  branch, from `git worktree list --porcelain`. The lease gains no key. git checks a branch out in
  one worktree at a time, so taking a branch checked out elsewhere, the main checkout included,
  fails with an error naming that worktree. `release` leaves the branch and checks out base
  detached, so committed work stays on the branch and the slot counts as clean (A5). The create
  hook's take has no name, and its slots stay detached ((e) Lease; (f) transitions 5 and 19).
- **19. The seed when no slot is ready**
  ([#24](https://github.com/weslo/upool/issues/24)). (g) step 0 seeds later slots from "the
  freshest ready slot", but `take` clones only when no slot is ready, and with `ready = 1` the
  top-up after a handout usually finds none. What does a new slot seed from?

  **A19.** The first candidate that has no live Editor and passes the staleness check: the freshest
  ready slot; a `needs-reset` slot, after its reset; the main checkout. The chosen slot is held as
  `warming` while the copy reads it, so no session writes to it mid-copy. If none qualifies,
  nothing is cloned, and `take` or the top-up says why. A slot that its reset moved to another
  commit is usually stale, because the checkout gives changed scripts new mtimes, so it falls
  through to the next candidate until the stale-seed refresh exists ((f) transitions 1, 18 and 23;
  (g) steps 0 and 1).
- **20. What `unity cache key` is compared against**
  ([#25](https://github.com/weslo/upool/issues/25)). The key hashes the Editor version,
  `packages-lock.json` and the build target, all read from the checkout, so on its own it says
  nothing about the Library, and (e) stored no key.

  **A20.** The key before and after each checkout upool makes in a slot: (g) step 7, the resets in
  `release`, `take --confirm` and `refresh`, and `refresh`'s move to base. A change means the next
  open is a large reimport: upool says so and sets the slot's `reimport_pending`, and from M3 the
  next `upool open` charges that open as a cold import and clears the flag. `reimport_pending` is
  the one key this adds to (e); nothing implements `schema_version` 1 yet, so the version stays 1
  ((e) Slot; (f); (g) step 7).
- **21. What `init --slots N` does**
  ([#26](https://github.com/weslo/upool/issues/26)). design.md gives `init [--slots N]` but not
  whether N is a one-off count or a setting.

  **A21.** `init` clones N slots now (1 ≤ N ≤ `max_slots`) and writes nothing to config; slots after
  the first seed as A19 says, normally from `slot-1`. Keeping N slots ready is `ready`'s job in
  `.upool.toml`. Slots beyond `ready` become `prune` candidates after `shrink_after` idle, and
  `init`'s help says so (D-commands).

### Decided for M2

Stage 2 filed these as `decision-needed` issues because each blocks part of the M2 chain. All five
are answered (maintainer decision, 2026-10-20), and (a) and (b) follow the answers. Three of them
need captures first, which [#148](https://github.com/weslo/upool/issues/148) records.

- **11. WorktreeCreate wait** ([#16](https://github.com/weslo/upool/issues/16)). The pooled branch
  may wait up to 20 s for a slot. How long the desktop app waits for a create hook before failing
  was never measured.

  **A11.** Measure it before the pooled branch is built. A create hook that logs its start, sleeps
  30 s and then prints a path shows whether the app waits that long and, if not, when it gives up;
  the result is a capture ([#148](https://github.com/weslo/upool/issues/148)). The pooled branch's
  wait is then the smaller of 20 s and the measured limit less 5 s, so that the hook fails with
  upool's own message before the app gives up ((a);
  [#78](https://github.com/weslo/upool/issues/78)).
- **13. Pooled-repo match** ([#18](https://github.com/weslo/upool/issues/18)). Every capture had
  stdin `cwd` at the repository root. Must a pooled request's `cwd` equal the registered main
  checkout, or may it be a subdirectory?

  **A13.** A prefix. A request whose canonical `cwd` is a registered main checkout, or inside one,
  routes to that pool; where registered repositories nest, the innermost wins. A `cwd` inside
  `<repo>/.claude/worktrees/` is a slot or an agent worktree, not the main checkout, so it takes the
  pass-through branch. The match compares `internal/paths` keys with `paths.Within`, so it needs no
  git call from the disclaimed helper ((a); [#72](https://github.com/weslo/upool/issues/72)).
- **14. Other `SessionStart` sources** ([#19](https://github.com/weslo/upool/issues/19)). Only
  `source: "startup"` was captured. Should `take --confirm` behave differently on `resume`, `clear`
  or `compact`?

  **A14.** Yes. On `startup` it does everything (b) lists. On `resume`, `clear` and `compact` it
  appends `export UNITY_PROJECT_PATH=<slot>` to `CLAUDE_ENV_FILE` and, while the slot's lease is
  held, records its parent as the lease's holder PID, since a resumed session may run in a new CLI
  process. It does not re-copy the agent files, which would overwrite edits the session made to
  them, and does not reset or start `refresh`. Each source is captured first, with the hook's parent
  PID ([#148](https://github.com/weslo/upool/issues/148)), and the session-start hook is built
  against those captures ((b); [#77](https://github.com/weslo/upool/issues/77)).
- **15. CLI remove and `SessionEnd`** ([#20](https://github.com/weslo/upool/issues/20)). No
  `WorktreeRemove` or `SessionEnd` stdin exists for a logged-in CLI session, so those code paths
  have no fixture (D-unverified; S§8 A.9).

  **A15.** Capture, then build. Both are captured from a logged-in interactive `claude --worktree`
  session with `scripts/capture-hook.sh` ([#148](https://github.com/weslo/upool/issues/148)), and
  `SessionEnd`'s best-effort release is built and tested against that capture, as rule 1 requires
  ([#80](https://github.com/weslo/upool/issues/80)). If `SessionEnd` never fires there, #80 closes
  without code, and `WorktreeRemove` and `upool run` remain the releases. The `WorktreeRemove`
  capture fills (a)'s CLI column. It fired, and #80 closed without code all the same (A25).
- **22. Removing a pass-through worktree** ([#27](https://github.com/weslo/upool/issues/27)). The
  user-scope `WorktreeRemove` hook runs for every repository, and the app accepts exit 0 with the
  directory still present ((a)). What does it do with a worktree in a repository upool does not
  manage?

  **A22.** Remove it when clean: `git -C <cwd> worktree remove <worktree_path>` when the worktree is
  clean as (f) defines it; when dirty, keep it and say so on stderr; exit 0 either way. Leaving it
  for `prune` would need new state, since nothing records pass-through worktrees. The pass-through
  create branch already runs `git worktree add` from the same disclaimed helper, which the spike
  measured working (4634 ms), so the remove is the same kind of git write, and M2's gate checks it
  ((a); (f); [#74](https://github.com/weslo/upool/issues/74)).

### Decided during M2

Raised while an M2 issue was built, rather than filed by Stage 2.

- **23. Where an unconfirmed lease's origin is kept**
  ([#75](https://github.com/weslo/upool/issues/75)). An unconfirmed lease that expires sends its
  slot back to the state it was handed out from, and `take --confirm` runs the deferred reset only
  for a slot handed out from `needs-reset` (#76). Once the slot is `leased-unconfirmed`, though,
  nothing in (e) said which of `ready` or `needs-reset` it came from.

  **A23.** The lease records it, as `reset_pending`: `true` when the create hook's take hands out a
  `needs-reset` slot (transition 10), `false` for a `ready` one (transition 4). `take --confirm`
  reads it to run the deferred reset and clears it, and an expiry reads it to send the slot back to
  `needs-reset` rather than `ready` (transition 7). It needs no git call, and `schema_version` stays
  1, as it did for `reimport_pending` (A20) (maintainer decision, 2026-10-20; (e) Lease; (f)).

- **24. Whether the hooks need scripts**
  ([#163](https://github.com/weslo/upool/issues/163)). The design put a script in
  `~/.upool/hooks` between each hook and upool, because the spike saw a script under `~/Documents`
  fail from the desktop app's helper (S§2 T7.6). No one had tried a binary there. A desktop worktree
  session ran a user-scope create hook whose script, outside `~/Documents`, ran a upool built in a
  checkout under it: upool exited 0 and the app logged `Created hook-based worktree`. From the
  checkout, through `Claude.app/Contents/Helpers/disclaimer`, the helper the spike found running the
  hooks, `head` on a file and `sh` on a script both failed with `Operation not permitted`, but upool
  ran, directly and from `sh -c` (desktop app 3.1623.0, macOS 15.7.3;
  `evidence/exec-probe/hook.txt`, `evidence/exec-probe/helper.txt`,
  `evidence/logs/desktop-exec-probe-main.log`).

  **A24.** The helper cannot read a file under `~/Documents`, but it can run a binary there, so
  upool is its own hook command. `setup` registers `upool hook <event>` in exec form, by the path
  `PATH` finds upool at, and installs no scripts. A handler that runs `upool hook`, by any path, is
  upool's, so a later `setup` replaces it. What the hooks read still lives outside the protected
  folders, under `~/.upool/` (maintainer decision, 2026-10-20; (a); (e)). A26 moves the
  registration from `setup` to `init`, per project; the exec form and the path stay.

- **25. Whether `SessionEnd` releases a lease**
  ([#80](https://github.com/weslo/upool/issues/80)). A15 said to build `SessionEnd`'s best-effort
  release against its capture, and the capture (#148) shows what it would have to do. Its stdin
  names no slot: `cwd` is the main checkout and `transcript_path` is keyed by the repository, so
  only `session_id` would tie it to a lease, and only a CLI session's. `/clear` ends a session with
  `reason: "clear"` under the lease holder's id while the same CLI process carries on in the
  worktree. And the interactive session's `/exit` that sent it also sent `WorktreeRemove`, which
  ends the lease already. `claude -p --worktree`, the case it was meant for, was not captured.

  **A25.** upool registers no `SessionEnd` hook, and #80 closed without code. `WorktreeRemove`
  releases desktop and interactive CLI sessions. `upool run` is the release for `-p` sessions and
  other agents, and a `-p` session run without it is left to the dead-PID reclaim of `take` and
  `doctor --fix`, since its confirmed lease records the CLI's PID (A6; (f) transitions 13 and 14)
  (maintainer decision, 2026-10-20; (b); `hooks/cli-session-end.json`;
  `hooks/session-start-clear.json`).

- **26. Where upool's hooks are registered** ([#171](https://github.com/weslo/upool/issues/171)).
  The design registered them once per machine, in `~/.claude/settings.json`, because the spike saw a
  project-scope hook get "provenance not vouched" from the desktop app (S§8 A.2–A.5). The spike's
  own log shows the hook **ran**: the app skipped only the per-machine config overlay and auto-trust
  (`evidence/logs/desktop-probe-main.log`). A user-scope hook, with no matcher, runs for every
  repository on the machine. A probe on 2026-10-20 (desktop app 3.1623.0) registered `upool hook
  worktree-create` and `worktree-remove` and a `SessionStart` probe in a scratch repository's own
  `.claude/settings.local.json`, with no user-scope hooks. The create hook ran and upool made the
  worktree, inside `.claude/worktrees`; the app skipped the overlay and auto-trust; the project's
  `SessionStart` ran in the worktree session and its `CLAUDE_ENV_FILE` export reached it; the app
  passed `--project-config-root <main checkout>`; and on archive the remove hook removed the
  worktree (`evidence/logs/desktop-project-scope-main.log`,
  `evidence/project-scope-probe/probe.txt`).

  **A26.** upool's hooks, skill and instruction blocks are per project. `upool init` registers
  `upool hook <event>` for `WorktreeCreate`, `WorktreeRemove` and `SessionStart` in the project's
  `.claude/settings.local.json`, in exec form by the path `PATH` finds upool at, as `setup` did at
  user scope (A24). It installs the skill in the project's `.claude/skills/upool/` and
  `.agents/skills/upool/`, and upserts the block into its `CLAUDE.md` and `AGENTS.md`. `upool setup`
  goes, and upool never writes the user's own Claude Code settings. The skipped overlay costs
  nothing: the session-start confirm re-copies the agent files. The work is #173 (`init` registers
  the hooks), #174 (`setup` removed, the skill and blocks per project) and #175 (`doctor`)
  (maintainer decision, 2026-10-20; (a)).

  **Measured** ([#172](https://github.com/weslo/upool/issues/172), desktop app 3.1623.0, Claude Code
  2.1.304; `evidence/logs/desktop-project-scope-capture-main.log`,
  `evidence/project-scope-capture/capture.txt`). Three desktop worktree sessions under project-scope
  hooks, one started by the user and two by a session, logged `worktree=1940ms`, `129ms` and
  `154ms`, with upool's own create at 50 to 60 ms: the probe's 232804 ms did not recur, and its
  cause stays unknown. Hooks in `settings.local.json` and in a committed `settings.json` behaved
  alike: the app ran them, skipped the overlay and auto-trust both times, and no session it drove
  showed a difference for the skipped auto-trust. Each hook fired once per event: a worktree that
  carried its own copy of `settings.json` from its start, and one given a `settings.local.json`
  mid-session, never ran theirs, since the app runs the session with `--project-config-root` the
  main checkout. The create and remove hooks ran under `Claude.app/Contents/Helpers/disclaimer`, and
  `SessionStart` and `UserPromptSubmit` in the session's CLI process. `claude -p --worktree` ran the
  project's create and `SessionStart` hooks in its own process. So `init` registers in
  `settings.local.json`, which holds this machine's path to upool and stays out of git, and the
  slots' copies need no handling.

- **27. Whether a lease holds a `git worktree lock`**
  ([#181](https://github.com/weslo/upool/issues/181)). The design held each lease as a
  `git worktree lock` on its slot as well, as insurance, since the spike saw the app never lock or
  touch a slot a hook made (D-leases; S§8 A.4). The first M2 gate run (2026-10-21, desktop app
  3.1623.0, Claude Code 2.1.304) archived two desktop sessions in slots, each idle with its lease
  confirmed. Each time, the app checked for a lock before it ran any hook, logged
  ``Refusing to remove worktree … locked with `git worktree lock` (kept with its entry). Unlock it
  to let cleanup proceed.``, and never ran `WorktreeRemove`. Both slots stayed `leased` with their
  holders dead. Nothing in upool runs before that check, so upool cannot drop the lock in time
  (`evidence/logs/desktop-archive-lock-main.log`). The archive of an unlocked worktree a hook made
  ran the remove hook in #171 and #172.

  **A27.** No lease takes a `git worktree lock`: a lease is its `state.json` entry alone, with its
  holder PID, for the create hook's, `take`'s and `run`'s leases alike. Where a lease ends,
  `release`, the dead-PID reclaim and an unconfirmed lease's expiry still unlock a slot git has
  locked, which frees a slot an earlier upool locked. The insurance is not replaced: `git worktree
  remove` without `--force` already refuses a dirty tree, and upool checks a slot's `.git` backlink
  before it trusts the slot. The work is #182 (maintainer decision, 2026-10-21; (a); (e)).

- **28. What a desktop archive does with a dirty slot**
  ([#185](https://github.com/weslo/upool/issues/185)). The remove hook parks a dirty slot
  (transition 9), as the spike saw an archive with an untracked file fire it (S§8 A.8). The second
  M2 gate run (2026-10-21, desktop app 3.1623.0) archived its sessions through the app's session
  tool. For a slot holding an untracked file, the app logged `Archived session` and nothing more:
  no cleanup, no `WorktreeRemove`, and its record of the worktree kept. The slot stayed `leased` to
  a dead holder. Gate run 1 had shown what such a record does: the app refused the path to later
  sessions ("already in use by worktree …"), and deleting the session did not clear the record;
  resuming the session and archiving it with its slot clean did. The same dirty archive from the
  app's Archive button logged `LocalSessions.archive`, `Cleaning up worktree`, `Removing worktree`
  and `Removed hook-based worktree`: the hook parked the slot with the file kept, and the app
  dropped its record (`evidence/logs/desktop-dirty-archive-main.log`). Clean archives ran the hook
  both ways.

  **A28.** No code change. The Archive button runs the remove hook for a dirty slot, so a desktop
  session archived the usual way parks its slot (transition 9). A dirty archive through the app's
  session tool is outside what upool can see: its slot is left to the dead-PID reclaim of `take`
  and `doctor --fix` (transitions 13 and 14), and its path stays blocked in the app until the
  session is resumed and archived clean. The M2 gate archives with the Archive button (maintainer
  decision, 2026-10-21; (a)).

### Decided for M3

Answered on 2026-10-21, before M3's build began; questions 2, 12, 34 and 35 once M3's capture (#86) had recorded what they needed.

- **1. Unity CLI exit 7** ([#11](https://github.com/weslo/upool/issues/11)). No capture shows it,
  and nothing known produces it. What should upool do when a wrapped call returns it?

  **A1.** Treat it as a failure. Exit 7, like any Unity CLI exit that (d) has no row for, makes
  upool exit 1 (`ExitFailure`) with the CLI's `errors[]` passed through. `internal/output` gains no
  constant for 7 unless a capture shows it means something upool should act on (maintainer
  decision, 2026-10-21; (d)).

- **3. upool's exit 1 and 6** ([#13](https://github.com/weslo/upool/issues/13)). Is 1 the generic
  failure for every command, or only `which`'s "not in a slot"? Does upool pass the Unity CLI's
  exit 6 through from its wrappers?

  **A3.** 1 is every command's failure, as the M1 commands already use it. 6 means upool's resolved
  target is missing: no Unity project, or no Editor, at the slot. A wrapped call that exits 6 makes
  upool exit 6 for that reason, and every other Unity CLI exit maps to 1 (A1) (maintainer decision,
  2026-10-21; (d)).

- **8. Saved scene list** ([#14](https://github.com/weslo/upool/issues/14)). A headless Editor
  closed gracefully empties `Library/LastSceneManagerSetup.txt` (S§9 G.3), so `upool close` saves
  the file before a headless session and restores it after. Where is the copy kept in between?

  **A8.** In upool's state directory: a file under `~/.upool/pools/<repo-key>/` named after the
  slot. It sits outside the slot, so neither a reset nor `git clean` touches it, and the state
  schema does not change (maintainer decision, 2026-10-21; D-lifecycle).

- **9. Cold-import charge** ([#15](https://github.com/weslo/upool/issues/15)). Is a cold-importing
  Editor charged 4.5 GB instead of its 2.5 GB or on top of it, is 4.5 GB a setting, and is the
  `max_editors` formula in (h) the intended one?

  **A9.** Instead of, as a constant. An Editor's charge is 4.5 GB while its cold import runs and
  `editor_ram_gb` (2.5 GB) after. The spike measured the cold-importing Editor's whole process at
  4.2 to 4.5 GB (S§2 T5), so charging both would count it twice. (h)'s formula stands: 3 Editors at
  64 GB (maintainer decision, 2026-10-21; (h)).

- **29. The re-query after a first-call timeout**
  ([#28](https://github.com/weslo/upool/issues/28)). The first `eval` on a freshly opened GUI
  Editor timed out at 5,000 ms yet ran (S§8 C.3), and the design has the wrapper re-query for side
  effects rather than retry blindly. What is the re-query for `save_all` before `close`, the call
  upool makes most?

  **A29.** `save_all` once more. Saving scenes that are already saved changes nothing, so the second
  call is the re-query, and it needs no new capture (maintainer decision, 2026-10-21; D-editors).

- **30. Lane folder lists** ([#31](https://github.com/weslo/upool/issues/31)). design.md gave
  `.upool.toml` "lane folder lists" but named no keys and gave them no job; the lane hook decides by
  file extension.

  **A30.** Dropped. Lanes go by file extension alone, and `.upool.toml` keeps `ready`, `max_slots`
  and `base` (maintainer decision, 2026-10-21; D-lanes; D-commands).

- **33. How upool opens a headless Editor** ([#86](https://github.com/weslo/upool/issues/86)). The
  design gives `upool open` a `--headless` mode, and M3's capture asked for "the flag the CLI's help
  gives". `unity open --help` (CLI 1.0.0-beta.13) lists no headless flag: its options are the
  Editor's version, path and architecture, a build target, `--args` and `--wait`. Neither the spike
  report nor the design records how the spike launched its headless Editors (S§8 B.1, B.5).

  **A33.** `unity open <slot> --args "-batchmode"`: the Editor in batch mode without `-quit`, which
  keeps running, as the spike's headless Editors did (S§9 G). It stays a `unity open` call, so the
  CLI picks the project's Editor version and the verb table's targeting rules hold (#82).
  `-nographics` is not passed: it would make the Editor behave unlike the GUI one a person
  develops in (maintainer decision, 2026-10-21; D-editors; (c)).

- **2. Unrecorded exit codes** ([#12](https://github.com/weslo/upool/issues/12)). The spike
  recorded no exit for `AMBIGUOUS_EDITOR`, `INVALID_COMMAND_ARGS`, the first-eval `COMMAND_FAILED`
  timeout, `build`'s "Not a Unity project" or `status` filtered to a project with no Editor. M3's
  capture (#86) recorded four of them: `INVALID_COMMAND_ARGS` exits 2, and the other three exit 6.
  The first-call timeout did not recur, so its exit is still unknown.

  **A2.** Key on codes, with the exits as recorded. Every wrapper decides a failure by
  `errors[].code`, never by the exit code, and the tests reply with the exits (d) now lists. The
  first-call timeout, whose exit no capture holds, is read by its code alone (maintainer decision,
  2026-10-21; (d)).

- **12. `unity open`'s warning line** ([#17](https://github.com/weslo/upool/issues/17)). The
  spike's `open` capture had the Hub-registry `Warning: …` line before the JSON, recorded with both
  streams together. M3's capture, streams apart, shows it on stderr, with stdout the bare envelope
  repeating it in `warnings` (`unity/open-gui-streams.stderr.txt`).

  **A12.** The `open` wrapper decodes stdout as the bare envelope, as every other verb's; the
  warning reaches upool through `warnings`. No tolerance for text before the JSON is needed
  (maintainer decision, 2026-10-21; (d)).

- **34. `release` when `projects verify --strict` fails**
  ([#29](https://github.com/weslo/upool/issues/29)). design.md runs `unity projects verify --strict`
  before release but says nothing of a failure. M3's capture shows it exits 0 on a clean slot and 6
  with `PROJECTS_VERIFY_FAILED` and a `META_MISSING` error when a `.meta` file is missing
  (`unity/projects-verify-pass.json`, `unity/projects-verify-fail.json`).

  **A34.** Warn, and release anyway. The problems go to stderr and the envelope's `warnings`, and
  the slot is released and exits 0. A clean slot's committed work lives on its branch, which
  `release` keeps, and a dirty slot is parked whatever verify says; refusing would leave
  `upool run`'s release on exit holding the slot (maintainer decision, 2026-10-21; D-lanes).

- **35. Whether `init` runs Unity's VCS set-up**
  ([#30](https://github.com/weslo/upool/issues/30)). design.md had `init` run
  `unity vcs doctor --fix`, `merge-setup` and `hooks install` on first registration, against its
  own rule of no repo-level executable hooks. On a throwaway clone (#86):
  - `vcs doctor --fix` exited 4, with 3 of 5 findings repaired.
  - It added 27 LFS patterns to `.gitattributes`, after which git showed 717 tracked binaries as
    modified.
  - It added 49 `.gitignore` rules and installed four LFS git hooks.
  - `merge-setup` set the UnityYAMLMerge merge driver in `.git/config`
    (`unity/vcs-doctor-fix.json`, `unity/vcs-merge-setup.json`).

  **A35.** None. `init` runs none of the three, and leaves the repository's VCS set-up to its
  owner. `doctor` reports what `unity vcs doctor` finds, `merge-setup`'s state included, and the
  commands that would fix it (maintainer decision, 2026-10-21; D-lanes; D-commands).

### Decided for M4

- **16. GoReleaser `brews`** ([#21](https://github.com/weslo/upool/issues/21)). The design asks
  for a `brews` section that writes `Formula/upool.rb`, and GoReleaser 2.10 and later deprecate it
  in favour of `homebrew_casks`. A cask installs the binary quarantined, and the binary is not
  signed, so a cask would likely fail every worktree session's create hook.

  **A16.** Keep the formula. `brews` stays, and release.yml pins GoReleaser to a version that still
  supports it; #104 makes the pin. Signing and notarizing, which a cask would need, wait until a
  reason to move appears (maintainer decision, 2026-10-21; design.md, "Repository, CI and Homebrew
  distribution").

- **31. `prune --include-*` overrides** ([#32](https://github.com/weslo/upool/issues/32)). design.md
  gives `prune` "narrow `--include-*` overrides instead of a single `--force`" but names none.

  **A31.** None in v0.4.0. `prune` removes only idle, merged, clean slots and collectable agent
  worktrees; parked and unmerged slots are left to a person. #103 closes unbuilt (maintainer
  decision, 2026-10-21; D-disk).

- **32. How `status --du` measures a slot** ([#33](https://github.com/weslo/upool/issues/33)). On
  APFS, `du` counts a cloned file's allocated blocks in full even where they are shared with the
  source, so it overstates a slot's cost; the spike measured cost by free-space deltas (S§4).

  **A32.** By private size: the sum over the slot's files of `getattrlist`'s
  `ATTR_CMNEXT_PRIVATESIZE`, the bytes no other clone shares, which is what deleting the slot
  frees. `max_disk` compares the same sum. A probe on 2026-10-21 (macOS 15.7.3, APFS): a 20 MB file
  and its `cp -c` clone each reported 0 private bytes against `du`'s 20 MB each; after 2 MB of the
  clone was rewritten, each reported 2 MB (`evidence/apfs-private-size/probe.txt`). It needs one
  call per file and no new dependency, so #99 times it on a real slot's `Library` (maintainer
  decision, 2026-10-21; D-disk).

### Other open questions

None: every question raised so far is answered above.

[^1]: The design (D-route) calls the desktop `session_id` "the app's own id" and the spike (S§2 TB)
    writes it as `local_<id>`. The captured stdin carries the bare UUID (`e8c6f3e6-…`); only
    `main.log` adds the `local_` prefix (compare `hooks/sibling-session1-create.json` with
    `evidence/logs/sibling-slot-main.log`). Per the spike's own captures, strip `local_` before comparing.
[^2]: The design (D-env) says the export "reached the Bash tool in both the CLI and the desktop app on
    2.1.295". The spike confirmed delivery only through the desktop app (S§2 T7.5; S§7); the
    standalone CLI run stopped at "Not logged in" before any Bash call (S§2 T7.4; S§8 A.9). The spike
    wins: delivery through a standalone CLI session is unverified.
[^3]: Neither document names these states. `creating` is the design's "acquisition incomplete"
    entry (D-leases) and `warming` is `refresh`'s re-warm (D-commands); A4 settles both.
[^4]: The design gives the defaults and the result ("at 64 GB that admits three Editors with headroom
    for one cold import", D-editors) but not a formula; this is the closed form that reproduces it.
    The spike's figure is "3 at 64 GB is comfortable, 4 is plausible" (S§8 "What this changes
    (updated)"), which the formula respects. A9 keeps it.
