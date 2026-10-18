# testdata

These files are the contract. Each one was captured on a real Mac during the spike
(2026-10-11 to 2026-10-12, macOS 15.7.3, Unity CLI 1.0.0-beta.13, Claude Code 2.1.295, desktop app
2.31226.1) and is reproduced verbatim: JSON pretty-printed with `jq .`, text copied byte for byte.
[docs/contracts.md](../docs/contracts.md) explains what each one proves; the "Section" column below
points into [docs/spike-report.md](../docs/spike-report.md).

**Never edit a fixture.** If upool's parsing has to change, the change must be matched by a new
capture from a real Editor or a real Claude Code session, added as a new file with its source
recorded here. A fixture that disagrees with the code is a bug report against the code, not the
fixture.

The "Spike log" column names the file in the spike's `logs/` folder each fixture came from. That
folder is not in this repository.

## hooks/

Hook stdin as Claude Code delivered it. Paths and ids are the spike machine's.

| Fixture | Spike log | Section | What it is |
|---|---|---|---|
| `cli-session-create.json` | `create-input.json` | §2 T7.4–T7.5; §5 | `WorktreeCreate` from `claude -p --worktree probe` |
| `cli-session-start.json` | `session-input.json` | §2 T7.5; §5 | `SessionStart` of the same CLI session, `cwd` = the new worktree |
| `desktop-session-create.json` | `test-e/create-1791854803.json` | §9 E.5 | desktop worktree session handed the managed slot `.claude/worktrees/slot-1` |
| `session-start.json` | `test-e/session-1791854804.json` | §9 E.5 | `SessionStart` of that session, `cwd` = `slot-1` |
| `desktop-session-create-refused.json` | `test-e/create-1791854824.json` | §9 E.7 | create while `slot-1` was in use; the app refused the printed path |
| `desktop-session-remove.json` | `test-e/remove-1791854849.json` | §9 E.6 | `WorktreeRemove` on archive of the first session |
| `desktop-session-create-reuse.json` | `test-e/create-1791854955.json` | §9 E.7 | create after archive; `slot-1` accepted again |
| `session-start-reuse.json` | `test-e/session-1791854955.json` | §9 E.7; §9 E (hook source) | `SessionStart` of the reuse session |
| `desktop-session-remove-reuse.json` | `test-e/remove-1791854966.json` | §9 E.7 | `WorktreeRemove` on archive of the reuse session |
| `subagent-create.json` | `test-f/create-1791857734892668000.json` | §9 F.2–F.3 | subagent `WorktreeCreate`: `prompt_id`, `scratchpad_dir`, `name` = `agent-<17 hex>` |
| `subagent-create-2.json` | `test-f/create-1791857734892674000.json` | §9 F.2–F.3 | the sibling subagent from the same prompt |
| `desktop-probe-create.json` | `desktop-probe/create-input-1791768286.json` | §2 TB; §5 | desktop create with a project-scope hook that made a sibling git worktree |
| `desktop-probe-session-start.json` | `desktop-probe/session-input-1791768291.json` | §2 TB; §5 | `SessionStart` in that sibling worktree |
| `desktop-probe-remove.json` | `desktop-probe/remove-input-1791768378.json` | §2 TB; §5 | `WorktreeRemove` on archive of that session |
| `desktop-no-worktree-session-start.json` | `desktop-probe/session-input-1791768354.json` | §2 TB (worktree unchecked) | `SessionStart` of a desktop session with no worktree, `cwd` = the repo |
| `sibling-session1-create.json` | `test-a/create-1791774165.json` | §8 A.5 | user-scope hook handing out the sibling slot `ta` |
| `sibling-session1-start.json` | `test-a/session-1791774166.json` | §8 A.5 | `SessionStart` of session 1 in `ta` |
| `sibling-refused-create-1.json` … `-4.json` | `test-a/create-1791774211.json`, `-216`, `-241`, `-261` | §8 A.7 | four creates while `ta` was in use; all refused, no remove followed |
| `sibling-session1-remove.json` | `test-a/remove-1791774754.json` | §8 A.6 | remove on archive of session 1; the directory stayed |
| `sibling-session2-create.json` | `test-a/create-1791774763.json` | §8 A.7 | session 2 handed `ta` again |
| `sibling-session2-start.json` | `test-a/session-1791774764.json` | §8 A.7 | `SessionStart` of session 2 |
| `sibling-session2-remove.json` | `test-a/remove-1791774778.json` | §8 A.7 | remove on archive of session 2 |
| `sibling-session3-create.json` | `test-a/create-1791774790.json` | §8 A.8 | session 3, which left an untracked file |
| `sibling-session3-start.json` | `test-a/session-1791774790.json` | §8 A.8 | `SessionStart` of session 3 |
| `sibling-session3-remove-dirty.json` | `test-a/remove-1791774813.json` | §8 A.8 | remove of the dirty slot; see `git/remove-status-untracked.txt` |
| `sibling-b4-create.json` | `test-a/create-1791844966.json` | §8 B.4 | desktop session in `ta` with three Editors up |
| `sibling-b4-start.json` | `test-a/session-1791844966.json` | §8 B.4 | its `SessionStart` |
| `sibling-b4-remove.json` | `test-a/remove-1791844977.json` | §8 B.4 | its remove on archive |

## unity/

Unity CLI 1.0.0-beta.13 output with `--format json`. Exit codes are in
[docs/contracts.md](../docs/contracts.md) (d); where the spike did not record one, the contract says
so.

| Fixture | Spike log | Section | What it is |
|---|---|---|---|
| `status-three-editors.json` | `b1-status.json` | §8 B.1 | `unity status`, three headless Editors `ready` |
| `status-one-editor.json` | `t6-targeting.txt`, A | §2 T6.4 | `unity status` from `/tmp`, one batch Editor |
| `status-until-ready-ready.json` | `t6-targeting.txt`, B | §2 T6.4 | `status --until-ready --timeout 120` with `UNITY_PROJECT_PATH`, ready at once |
| `status-until-ready-no-instances.json` | `t6-targeting.txt`, J | §2 T6.5 | `status --project-path <missing> --until-ready --timeout 15`: `STATUS_NO_INSTANCES` after 15.11 s, exit 6 |
| `recompile-up-to-date.json` | `t6-targeting.txt`, C | §2 T6.4 | `unity recompile` with the variable: `up_to_date` (no top-level `errors`/`warnings` in the capture) |
| `command-eval-no-pipeline-instance.json` | `t6-targeting.txt`, E2 | §2 T6.4 | `eval` with the variable naming a project with no Editor: `COMMAND_FAILED` |
| `command-eval-misrouted.json` | `t6-targeting.txt`, G2 | §2 T6.4 | `eval` from inside `clone2` (no Editor) ran in `clone1`'s Editor |
| `command-eval-main-thread-timeout.json` | `c3-dirty.json` | §8 C.3 | first `eval` on a fresh GUI Editor: `COMMAND_FAILED` timeout, though the code ran |
| `command-save-all.json` | `c5-save-all.json` | §8 C.5 | `unity command save_all`: `saved: true` |
| `command-list-query-save.json` | `c2-query-save.json` | §8 C.2 | `unity command --query save` command listing |
| `command-list-query-refresh.json` | `d2-query-refresh.json` | §8 D.2 | `unity command --query refresh`: only `package_resolve` |
| `close-headless.json` | `t6-targeting.txt`, I | §2 T6.4 | `unity close <path>` on a batch Editor: `graceful` |
| `close-gui.json` | `c4-close.json` | §8 C.4 | `unity close <path>` on a GUI Editor with a dirty scene: `graceful`, work discarded |
| `open-gui.json` | `c1-open-clone1.json` (JSON part) | §8 C.1 | `unity open <path>`: `launchedPid`, Hub-registry warning |
| `open-gui.raw.txt` | `c1-open-clone1.json` (whole file) | §8 C.1 | the same capture as recorded, with the `Warning:` line before the JSON |
| `test-not-a-unity-project.json` | `t6-test.json` | §2 T6.6 | `unity test` from `/tmp` with the variable: `COMMAND_FAILED`, exit 6 |
| `test-positional.json` | `t6-test-positional.json` | §2 T6.6 | `unity test <path> --mode EditMode`: success |
| `run-not-a-unity-project.json` | `b3-run.json` | §8 B.3 | `unity run --command editor_status` from `/tmp` with the variable: `COMMAND_FAILED`, exit 6 |
| `cache-key-project-path-invalid.json` | `d1-cache-key.txt`, `--project-path form` | §8 D.1 | `unity cache key --project-path`: `INVALID_COMMAND_ARGS` |
| `projects-before.json` | `projects-before.json` | §2 T6.3 | `unity projects` (Hub registry) before `--seed cache` |
| `projects-after-seed-cache.json` | `projects-after-seed-cache.json` | §2 T6.3 | the same after `--seed cache` registered the worktree |
| `vcs-worktree-add-dry-run.json` | `t6-dryrun.json` | §2 T6.2 | `unity vcs git worktree add --seed cache --dry-run`; home directory redacted |
| `vcs-worktree-add-seed-cache.json` | `t6-seed-cache.json` | §2 T6.3 | `--seed cache`: `PackageCache` withheld |
| `vcs-worktree-add-seed-full.json` | `t6-seed-full.json` | §2 T6.3 | `--seed full` |
| `vcs-worktree-remove-seed-cache.json` | `t6-remove-seed-cache.json` | §2 T6.3 | `unity vcs git worktree remove -y` |
| `vcs-worktree-remove-seed-full.json` | `t6-remove-seed-full.json` | §2 T6.3 | the same for the `--seed full` worktree |

### unity/projections/

The spike recorded only a `jq` projection of these responses, not the full envelope. The fields
present are verbatim; fields missing from a projection were dropped by the spike's `jq` filter, not
by the CLI. Use them for codes and messages only.

| Fixture | Spike log | Section | What it is |
|---|---|---|---|
| `command-eval-ambiguous-editor.json` | `b-targeting.txt`, B2c | §8 B.2 | `eval` from `/tmp` with three Editors up: `AMBIGUOUS_EDITOR` with the full message |
| `command-eval-ambiguous-editor-codes.json` | `b-targeting.txt`, B2f | §8 B.2 | `eval` from `clone3` (no Editor) with three up: error codes only |
| `command-list-no-pipeline-instance.json` | `t6-targeting.txt`, E | §2 T6.4 | `unity command` with the variable naming a project with no Editor (exit 6) |
| `status-env-no-instances.json` | `t6-targeting.txt`, H | §2 T6.4 | `unity status` with the variable naming a project with no Editor |
| `status-project-path-no-instances.json` | `t6-targeting.txt`, H2 | §2 T6.4 | the same with `--project-path` |
| `status-until-ready-closed-project.json` | `t6-targeting.txt`, J2 | §2 T6.5 | `--until-ready --timeout 15` on an existing project with no Editor: 15.12 s |
| `build-not-a-unity-project.json` | `b-targeting.txt`, B3c | §8 B.3 | `unity build --list-profiles` from `/tmp` with the variable |
| `cache-key-data.json` | `d1-cache-key.txt`, run1 | §8 D.1 | the key object of `unity cache key`; run2, after a `.cs` edit, after revert and on main were byte-identical |

## logs/

`~/Library/Logs/Claude/main.log` excerpts from the desktop app, copied as the spike recorded them.

| Fixture | Spike log | Section | What it shows |
|---|---|---|---|
| `desktop-probe-main.log` | `desktop-probe/main-log-excerpt.txt` | §2 T7.6; §2 TB; §5 | the hook under `~/Documents` failing (`Operation not permitted`, `worktree_hook_failed`); a project-scope hook's overlay and auto-trust skipped as "provenance not vouched"; the archive sequence |
| `sibling-slot-main.log` | `test-a/main-log-excerpt.txt` | §8 A.5–A.8 | user-scope hook outside the managed directory: overlay and auto-trust skipped for location; four "already in use" refusals; archive and reuse sequences; `worktree=36ms`–`56ms` timings |
| `managed-slot-main.log` | `test-e/main-log-excerpt.txt` | §9 E.5–E.7 | slot in `.claude/worktrees`: overlay lines, "Auto-trusted worktree … (inherited from …)", one refusal, archive and reuse |
| `subagent-session-main.log` | `test-f/main-log-excerpt.txt` | §9 F.3 | the subagent test's session: no worktree lines at all, because subagent worktrees never reach the app |

## git/

Git output that the release and remove rules are built on.

| Fixture | Spike log | Section | What it is |
|---|---|---|---|
| `projectsettings-define-reorder.diff` | `d3-clone1-projectsettings.diff` | §8 D.3; §9 G | the only observed define-order churn: `scriptingDefineSymbols` reordered, same set; the noise rule reverts exactly this |
| `remove-status-untracked.txt` | `test-a/remove-status-1791774813.txt` | §8 A.8 | `git status --porcelain` taken by the remove hook after session 3: `?? spike-reuse.txt`; the slot must be parked |

## Captured but not copied

Kept only in the spike's `logs/` folder, because nothing in upool parses them: the Unity Editor logs
(`*-run*.log`, `cold*.log`, `conc*.log`, `mem-*.log`, `b-headless-*.log`, `headless.log`,
`churn-batch.log`, `g-repro-headless.log`, `main-after.log`, `wtclone-run*.log`), `commands.log`,
`measurements.txt`, `setup.txt`, the `--help` texts, the probe hook scripts and their own logs
(`fired.log`, `probe.log`, `git.log`, `selftest.log`), the desktop app's `git-worktrees.json`
snapshots (`e4-*`, `test-e/registry/`, `test-gc/`), `symlink-check.tsv`, `d4-settings-diff.txt`,
`c5-startup-scene.diff`, `t6-test-results.xml`, `t6-dryrun-human.txt`, `t7-claude-p.*`, and the
`jq` projections of successful calls (`b-targeting.txt` B2a–B2g except B2c and B2f, B3d;
`t6-targeting.txt` D, D2, F, F2, G; `b5-close.txt`). The spike report quotes what matters from them.
