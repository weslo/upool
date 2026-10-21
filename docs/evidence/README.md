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
| `desktop-exec-probe-main.log` | none: #163 | contracts.md A24 | a create hook running a upool built under `~/Documents`: "Created hook-based worktree" and the session started (desktop app 3.1623.0) |

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
