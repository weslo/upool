# evidence

Spike output, and that of later probes, that backs [contracts.md](../contracts.md) but that no
test reads: the desktop app's log excerpts, `jq` projections of Unity CLI replies whose full
envelopes were not kept, and the exec probe's transcripts. Paths in them are written as the
variables in [testdata/README.md](../../testdata/README.md), and, like the captures, these files are
copied verbatim and never edited. The "Section" column points into
[spike-report.md](../spike-report.md), or names the decision a later probe backs; the "Spike log"
column names the file in the spike's `logs/` folder, which is not in this repository.

## logs/

`~/Library/Logs/Claude/main.log` excerpts from the desktop app, copied as the spike recorded them.
upool never reads this log; these are for people, and for the manual gates that say which lines to
look for.

| File | Spike log | Section | What it shows |
|---|---|---|---|
| `desktop-probe-main.log` | `desktop-probe/main-log-excerpt.txt` | §2 T7.6; §2 TB; §5 | the hook under `~/Documents` failing (`Operation not permitted`, `worktree_hook_failed`); a project-scope hook's overlay and auto-trust skipped as "provenance not vouched"; the archive sequence |
| `sibling-slot-main.log` | `test-a/main-log-excerpt.txt` | §8 A.5–A.8 | user-scope hook outside the managed directory: overlay and auto-trust skipped for location; four "already in use" refusals; archive and reuse sequences; `worktree=36ms`–`56ms` timings |
| `managed-slot-main.log` | `test-e/main-log-excerpt.txt` | §9 E.5–E.7 | slot in `.claude/worktrees`: overlay lines, "Auto-trusted worktree … (inherited from …)", one refusal, archive and reuse |
| `subagent-session-main.log` | `test-f/main-log-excerpt.txt` | §9 F.3 | the subagent test's session: no worktree lines at all, because subagent worktrees never reach the app |
| `desktop-project-scope-main.log` | none: #171 | contracts.md A26 | a create hook registered in a scratch repository's own `.claude/settings.local.json`: "Created hook-based worktree", the overlay and auto-trust skipped as "provenance not vouched", `--project-config-root` the main checkout, `worktree=232804ms`, and the remove on archive (desktop app 3.1623.0) |
| `desktop-project-scope-capture-main.log` | none: #172 | contracts.md A26 | three desktop worktree sessions under project-scope hooks, `settings.local.json` then `settings.json`: their creates (`worktree=1940ms`, `129ms`, `154ms`), the overlay and auto-trust skipped each time, `--project-config-root` the main checkout, and the removes on archive (desktop app 3.1623.0) |
| `desktop-exec-probe-main.log` | none: #163 | contracts.md A24 | a create hook running a upool built under `~/Documents`: "Created hook-based worktree" and the session started (desktop app 3.1623.0) |
| `desktop-dirty-archive-main.log` | none: #185 | contracts.md A28 | a slot path refused as "already in use" by a record the app kept; a dirty slot archived through the app's session tool (`Archived session` and nothing more), then resumed and archived clean; and a dirty slot archived with the Archive button (`LocalSessions.archive`, the cleanup and `Removed hook-based worktree`) (desktop app 3.1623.0) |
| `desktop-archive-lock-main.log` | none: #181 | contracts.md A27 | the first M2 gate run: two desktop sessions handed slots by upool's project-scope create hook (`worktree=121296ms`, then `75ms`), then archived, each refused as "locked with `git worktree lock` (kept with its entry)" with no remove hook run (desktop app 3.1623.0) |
| `desktop-session-end-main.log` | none: #192 | #192, step 1 | a throwaway pooled project's desktop sessions: two deleted while live, each running the remove hook (`LocalSessions.delete`, then the cleanup and `Removed hook-based worktree`); a quit (`Stopping 12 active session(s) on quit`) and a crash, neither running one; the quit sessions resumed; and a new session refused twice for the same slot, `already in use by worktree "pwd-03f0cb"` then `worktree_hook_failed`, 1 m 47 s apart (desktop app 3.6556.2) |

## exec-probe/

The probe behind [contracts.md](../contracts.md) A24
([#163](https://github.com/weslo/upool/issues/163), 2026-10-20; desktop app 3.1623.0, Claude Code
2.1.304, macOS 15.7.3), recorded as it ran, with paths as variables. Its `main.log` lines are
`logs/desktop-exec-probe-main.log`.

| File | What it shows |
|---|---|
| `hook.txt` | the user-scope create hook, a script outside `~/Documents` that ran `bin/upool hook worktree-create` from a checkout under it; its log (exit 0), stdin, stdout and empty stderr; and its parent process, `Claude.app/Contents/MacOS/Claude` |
| `helper.txt` | the same checkout, through `Claude.app/Contents/Helpers/disclaimer` from a shell: `head` on a file and `sh` on a script fail with `Operation not permitted`, and upool runs, directly and from `sh -c` |

## unity-projections/

The spike recorded only a `jq` projection of these Unity CLI 1.0.0-beta.13 replies, not the full
envelope. The fields present are verbatim; fields missing from a projection were dropped by the
spike's `jq` filter, not by the CLI, and some list errors as bare strings. They cannot be replayed,
and their codes and messages are already in [contracts.md](../contracts.md) (d). A full capture of
each is still wanted (contracts.md, open question 2).

| File | Spike log | Section | What it is |
|---|---|---|---|
| `command-eval-ambiguous-editor.json` | `b-targeting.txt`, B2c | §8 B.2 | `eval` from `/tmp` with three Editors up: `AMBIGUOUS_EDITOR` with the full message |
| `command-eval-ambiguous-editor-codes.json` | `b-targeting.txt`, B2f | §8 B.2 | `eval` from `clone3` (no Editor) with three up: error codes only |
| `command-list-no-pipeline-instance.json` | `t6-targeting.txt`, E | §2 T6.4 | `unity command` with the variable naming a project with no Editor (exit 6) |
| `status-env-no-instances.json` | `t6-targeting.txt`, H | §2 T6.4 | `unity status` with the variable naming a project with no Editor |
| `status-project-path-no-instances.json` | `t6-targeting.txt`, H2 | §2 T6.4 | the same with `--project-path` |
| `status-until-ready-closed-project.json` | `t6-targeting.txt`, J2 | §2 T6.5 | `--until-ready --timeout 15` on an existing project with no Editor: 15.12 s |
| `build-not-a-unity-project.json` | `b-targeting.txt`, B3c | §8 B.3 | `unity build --list-profiles` from `/tmp` with the variable |
| `cache-key-data.json` | `d1-cache-key.txt`, run1 | §8 D.1 | the key object of `unity cache key`; run2, after a `.cs` edit, after revert and on main were byte-identical |

## project-scope-probe/

The probe behind [contracts.md](../contracts.md) A26
([#171](https://github.com/weslo/upool/issues/171), 2026-10-20; desktop app 3.1623.0, Claude Code
2.1.304, macOS 15.7.3), recorded as it ran, with paths as variables. Its `main.log` lines are
`logs/desktop-project-scope-main.log`.

| File | What it shows |
|---|---|
| `probe.txt` | the scratch repository's `.claude/settings.local.json`, registering upool for `WorktreeCreate` and `WorktreeRemove` and a probe for `SessionStart`; the probe's log of the `SessionStart` stdin, `cwd` the worktree; the session's reply, `UPOOL_PROBE` exported by the probe; and the worktree gone after the archive, its branch kept |

## project-scope-capture/

The capture behind [contracts.md](../contracts.md) A26's measurements
([#172](https://github.com/weslo/upool/issues/172), 2026-10-20; desktop app 3.1623.0, Claude Code
2.1.304, macOS 15.7.3), recorded as it ran, with paths as variables. Its `main.log` lines are
`logs/desktop-project-scope-capture-main.log`.

| File | What it shows |
|---|---|
| `capture.txt` | the logger every hook ran through and the two settings files; its log of each hook's start and end to the millisecond, its source (`local` or `shared`) and its parent process, for three desktop sessions, a second prompt after a hook was written into a worktree's own settings, a `claude -p --worktree` run and two archives; and the CLI run's output |

## apfs-private-size/

The probe behind [contracts.md](../contracts.md) A32
([#33](https://github.com/weslo/upool/issues/33), 2026-10-21; macOS 15.7.3, APFS), recorded as it
ran.

| File | What it shows |
|---|---|
| `probe.txt` | a small C program that prints `getattrlist`'s `ATTR_CMNEXT_PRIVATESIZE` for each path, and its run: a 20 MB file and its `cp -c` clone at 0 private bytes each while `du` reports 20 MB each; both at 2 MB after 2 MB of the clone was rewritten; and the original at 20 MB again once the clone is deleted |

## session-context-probe/

The probe behind [contracts.md](../contracts.md) A43
([#228](https://github.com/weslo/upool/issues/228) and
[#235](https://github.com/weslo/upool/issues/235), 2026-10-23 and 2026-10-24; Claude Code 2.1.305
for the `claude -p` runs and 2.1.308, the desktop app's bundled CLI, for the desktop session,
though `probe.txt`'s second line names only 2.1.305; desktop app 3.6556.2), recorded as it ran, with
paths as variables. The probe is `scripts/sessionprobe`, in three builds:
- the runs logged up to 20261023T091947, `c1` to `c7`, used an earlier build whose context had no
  `-<source>` suffix and whose log had no `event=` or `claude_project_dir=` field (`probe-c8` is
  the worktree `c7` ran in);
- the runs from 20261023T092029, `c9`, `c10` and the desktop session, used a build that added the
  suffix, the `main` file and `claude_project_dir=`;
- the `SubagentStart` run, `c11`, and #235's imports runs, used the build in the repository, which
  adds `event=`. The probe's log, which `imports.txt` does not transcribe, gives the imports runs'
  lines (20261024T174422 and 20261024T174434) `event=SessionStart`, and the binary was last built
  just before `c11`. `imports.txt`'s "as for its desktop run" describes how the probe was built and
  registered, not which build ran.

| File | What it shows |
|---|---|
| `probe.txt` | The scratch repository's hooks and the probe's log, one line per hook run. It holds every `claude -p` answer: the context reaching the model as JSON `additionalContext` and as plain stdout, whole at 3.5 KB, and again on resume (the resumes' `cwd` was the main checkout, not the worktree, despite the file's opening line); the hook firing on a compaction; a subagent without the context, and with it from a `SubagentStart` hook; and a `CLAUDE.md` the hook copied into the worktree not loading in that session, but loading in the next. It also holds the maintainer's desktop session: its answers, with `/clear` starting a new session whose `source` is `startup` and a `compact` context reaching the model; its transcripts' `hook_additional_context` attachments; and the hook stdin the probe recorded |
| `imports.txt` | #235's run: a file `CLAUDE.md` imports with `@`, written into the worktree by a second `SessionStart` hook, not read in that session (`grove unknown`), and read by the next, which found it at the start (`grove olive`) |

## session-end-probe/

#192's step 1 (2026-10-27; desktop app 3.6556.2, whose sessions ran Claude Code 2.1.308; macOS
15.7.3), recorded as it ran: which ways a desktop session in a slot can end without
`WorktreeRemove`, and what upool and the app are left with. `${repo}` here is a throwaway Unity
project made for the probe, pooled by a upool built from the `issue-192` branch, its `max_slots`
raised to 6 after the first event. Each file is a snapshot of `upool status`, `state.json`'s leases
and whether each holder PID was alive, each slot's `git status`, and the app's record of each slot
in its `git-worktrees.json`. The app's log for the whole run is `logs/desktop-session-end-main.log`.

| File | What it shows |
|---|---|
| `01-setup.txt` | the pool after `upool init`: slot-1 `ready`, and no record in the app |
| `02-e1-live.txt`, `03-e1-deleted.txt` | event 1: sessions A (slot-1, clean) and B (slot-2, an untracked file) live, then both deleted from the sidebar. The remove hook ran for each: slot-1 `needs-reset`, slot-2 `parked`, and both records gone. Slot-2 was then removed by hand, which left it `parked` in `state.json` |
| `04-e2-live.txt`, `05-e2-relaunched.txt` | event 2: sessions C (slot-1, clean) and D (slot-3, dirty) live, then the app quit with ⌘Q and relaunched. No remove hook ran: both holders gone, both leases and both records kept |
| `06-e2-sessionE.txt` | a new session E handed the `ready` slot-4: the create hook reclaimed neither dead lease |
| `07-e2-resumed.txt` | C and D resumed: upool recorded each resumed process as its lease's holder, found by `session_id` |
| `08-e3-relaunched.txt` | event 3: C, D and E live, then the app's main process killed with `kill -9` and the app relaunched. As for the quit: three dead holders, their leases and records kept |
| `09-e3-cli-take.txt` | a Terminal `upool take`, which reclaims dead holders' leases: C's slot-1 reset and taken, D's slot-3 parked, E's slot-4 `ready`, though all three sessions could still be resumed |
| `10-e3-resumeC.txt` | after a new session was handed slot-4 and refused twice for E's record, with slot-5 `ready` throughout: C resumed in slot-1, on the Terminal take's branch, `probe-cli-take`, and recorded as the holder of the Terminal take's lease |
| `11-e3-archived.txt` | C, D and E archived with the Archive button: each record gone, and slot-1 `needs-reset`, ending the Terminal take's lease too |
