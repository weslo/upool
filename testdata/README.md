# testdata

Two kinds of file live here, under different rules:

- [`captures/`](#captures): what the outside world actually did. Each one was captured on a real
  Mac and is **never edited**.
- [`golden/`](#golden): what upool itself is expected to print. These are regenerated whenever
  upool's output changes on purpose.

## captures/

These files are the contract. Each one was captured on a real Mac, during the spike
(2026-10-11 to 2026-10-12, macOS 15.7.3, Unity CLI 1.0.0-beta.13, Claude Code 2.1.295, desktop app
2.31226.1) unless its row names a later source, and is reproduced verbatim except for paths: JSON
pretty-printed with `jq .`, text copied byte for byte. [docs/contracts.md](../docs/contracts.md)
explains what each one proves; the "Section" column below points into
[docs/spike-report.md](../docs/spike-report.md).

**Paths are variables.** No path from the capturing machine is committed. Each one is replaced by
the variable for its role, and a test fills the variables with the temporary directories it creates
(a fake repository, a fake home), so the captures describe the shape of the input, not one machine.
The replacement is consistent across files, so every relationship between paths survives it: stdin
`cwd` against `worktree_path`, a slot against its repository, a transcript key against its slot.

| Variable | Role | Example from a capture |
|---|---|---|
| `${home}` | the user's home directory | `${home}/.claude/projects/…` |
| `${repo}` | the pooled repository (the spike's Unity project, under `~/Documents`) | `${repo}/.claude/worktrees/slot-1` |
| `${repo_lc}` | `${repo}` lowercased, as the desktop app's registry stores it (docs only) | — |
| `${spike}` | the folder beside the repository that held the spike's sibling slots (`ta`, `clone1`…) and logs | `${spike}/ta` |
| `${home_key}`, `${repo_key}`, `${spike_key}` | Claude Code's project key for that path: every `/` and `.` replaced by `-` | `${repo_key}--claude-worktrees-slot-1` is the key of `${repo}/.claude/worktrees/slot-1` |
| `${claude_tmp}` | Claude Code's per-user temporary root, `/private/tmp/claude-<uid>` | `${claude_tmp}/${repo_key}/<session>/scratchpad` |
| `${cwd}` | the directory a Unity CLI command ran in | `Not a Unity project (ProjectVersion.txt not found at ${cwd}).` |

Only these braced names are variables. `internal/pathvars` fills them (`Fill`, for tests) and puts
them back (`Scrub`, for new captures) by literal string replacement, not `os.Expand`, because some
captures contain a literal `$schema` key; any variable `${x}` also gives `${x_key}` and `${x_lc}`.
`make check-paths`, run in CI, fails if a home-directory path or a macOS temporary directory appears
anywhere in the repository.

**Never edit a capture.** If upool's parsing has to change, the change must be matched by a new
capture from a real Editor or a real Claude Code session, with its paths replaced as above, added as
a new file with its source recorded here. A capture that disagrees with the code is a bug report
against the code, not the capture. `make check-captures`, run in CI, fails when a change modifies,
renames or removes anything under `captures/`; adding a file passes.

The "Spike log" column names the file in the spike's `logs/` folder each capture came from. That
folder is not in this repository.

### Re-capturing

The Unity CLI and Claude Code both change between releases, so the captures are meant to be taken
again: write the new ones to a scratch directory and diff them against `captures/` to see what
moved, and add any that upool needs as new files.

- **Unity CLI replies:** `scripts/capture-unity.sh <name> <unity arguments…>`, run in the state the
  "What it is" column below describes (which Editors are open, from which directory). It writes
  `<name>.json` (pretty-printed, or `<name>.raw.txt` when stdout is not JSON), `<name>.stderr.txt`
  when stderr is not empty, and `<name>.exit` with the exit code, all with paths replaced. It
  refuses to overwrite a file.
- **Hook stdin:** register a copy of `scripts/capture-hook.sh` for the event, outside `~/Documents`
  (the desktop app runs `WorktreeCreate` from a helper that cannot read there). It saves each stdin
  under `~/.upool-capture/` and then runs the hook it wraps, if any, on the same stdin; a
  `WorktreeCreate` capture must wrap a command that prints a worktree path, or the session aborts.
  Replace the paths with `go run ./scripts/scrubpaths --var repo=<path> … < <file>` before adding
  the file here.

Running either script needs a real Editor or the desktop app, so it is a manual check, never a
claim in a pull request.

### captures/hooks/

Hook stdin as Claude Code delivered it, with paths as variables. Session ids are the spike's.

| Capture | Spike log | Section | What it is |
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

Captures added after the spike come from `scripts/capture-hook.sh`, run during the manual-gate
check that the "Source" column names, with the desktop app's and Claude Code's versions. A row
marked **pending** is a capture that check has not added yet: whoever runs it names the file it
wrote, replaces **pending** with the versions and the date, and says what the capture showed; a
hook that never fired gets no file, and its row says so.

| Capture | Source | Versions | What it is |
|---|---|---|---|
| `desktop-create-timeout.txt` | [M2 gate](../docs/manual-gates/M2.md), "Capture the M2 hook inputs", step 3 | desktop app 3.1623.0, Claude Code 2.1.304, 2026-10-20 | not hook stdin: a create hook's log and the desktop app's `main.log` for a worktree session in the upool checkout whose create hook slept 30 s before printing its path. The app waited the whole 30 s and used the path (`Created hook-based worktree` at the `printed` time, `worktree=30210ms`), so its limit is above 30 s and A11's wait stays 20 s (A11) |
| `session-start-resume.json` | same, step 4 | desktop app 3.1623.0, Claude Code 2.1.304, 2026-10-20 | `SessionStart` of a desktop worktree session resumed after the app was quit and opened again, `source: "resume"`, `cwd` the worktree. Its parent PID was not the one `startup` and `compact` logged: a resumed session runs in a new CLI process (A14) |
| `session-start-clear.json` | same, step 5 | Claude Code 2.1.304, 2026-10-20 | `SessionStart` after `/clear` in an interactive `claude --worktree` session, `source: "clear"`: the same parent PID as its `startup`, a new `session_id`, and a `SessionEnd` with `reason: "clear"` for the old one; in a desktop session without a worktree, `/clear` sent none, starting a new CLI session whose `SessionStart` said `startup` (desktop app 3.1623.0, 2026-10-20) (A14) |
| `session-start-compact.json` | same, step 4 | desktop app 3.1623.0, Claude Code 2.1.304, 2026-10-20 | `SessionStart` after `/compact` in that desktop worktree session, `source: "compact"`: the same parent PID as its `startup`, and it carries `prompt_id`, `model` and `session_title` (A14) |
| `cli-session-remove.json` | same, step 5 | Claude Code 2.1.304 (the desktop app's bundled CLI), 2026-10-20 | `WorktreeRemove` when a logged-in interactive `claude --worktree a15-probe` session in a scratch repository ended with `/exit` and the worktree removed: unlike the desktop app's, its `cwd` is the worktree, not the main checkout, and it carries `prompt_id` and `scratchpad_dir`, so a `prompt_id` marks a subagent only on `WorktreeCreate` (A15) |
| `cli-session-end.json` | same, step 5 | Claude Code 2.1.304, 2026-10-20 | `SessionEnd` of that session: `reason: "prompt_input_exit"`, `cwd` the main checkout though the session ran in the worktree, with `prompt_id` and `scratchpad_dir`. Quitting the desktop app ended each of its sessions with `reason: "other"` (A15) |
| `pretooluse-edit.json` | [M3 gate](../docs/manual-gates/M3.md), "Capture the M3 replies", step 6 | desktop app 3.1623.0, Claude Code 2.1.304, 2026-10-21 | `PreToolUse` for `Edit` in a desktop worktree session in a slot, the hook registered in the project's `.claude/settings.local.json` beside upool's: `cwd` the slot, `tool_input` with `file_path`, `old_string`, `new_string` and `replace_all`, and `tool_use_id`, `prompt_id`, `scratchpad_dir`, `permission_mode` and `effort` beside the usual fields. The stdin the lane hook reads (#95) |
| `pretooluse-write.json` | same, step 6 | desktop app 3.1623.0, Claude Code 2.1.304, 2026-10-21 | `PreToolUse` for `Write` in the same session: `tool_input` with `file_path` and `content` |
| `pretooluse-notebookedit.json` | same, step 6 | desktop app 3.1623.0, Claude Code 2.1.304, 2026-10-21 | `PreToolUse` for `NotebookEdit` in the same session: `tool_input` with `notebook_path`, `cell_id` and `new_source` |
| `desktop-clear-session-start.json` | [#228](https://github.com/weslo/upool/issues/228)'s probe (`docs/evidence/session-context-probe/`) | desktop app 3.6556.2, Claude Code 2.1.308 (the desktop app's bundled CLI), 2026-10-23 | `SessionStart` after `/clear` in a desktop worktree session: `source: "startup"`, a new `session_id`, the same worktree as `cwd` (contracts (b); question 43) |
| `cli-resume-checkout-session-start.json` | same | Claude Code 2.1.305, 2026-10-23 | `SessionStart` of a `claude -p --resume` launched from the main checkout, for a session that ran in a hook-made worktree: `source: "resume"`, and `cwd` the main checkout, not the worktree, with `transcript_path` keyed by the main checkout (contracts (b); question 43) |
| `cli-subagent-start.json` | [#228](https://github.com/weslo/upool/issues/228)'s probe (`docs/evidence/session-context-probe/probe.txt`, run c11) | Claude Code 2.1.305, 2026-10-23 | `SubagentStart` for a general-purpose subagent of a `claude -p --worktree` session: `agent_id`, `agent_type`, `prompt_id` and `scratchpad_dir`, with `cwd` the session's worktree; the probe's `additionalContext` reached the subagent (contracts (b); A43) |

The file names say what kind of request each one is. The test for hook classification
(docs/contracts.md (a)) runs over every `.json` file here and checks each one against its name;
`desktop-create-timeout.txt`, a log rather than stdin, is the one file it passes over. A
`pretooluse-<tool>.json` name says its event and its tool, which the test checks; `PreToolUse`
runs in the session's CLI process, as `SessionStart` does, so its name says no kind of request.

### captures/unity/

Unity CLI 1.0.0-beta.13 output with `--format json`. Exit codes are in
[docs/contracts.md](../docs/contracts.md) (d); where the spike did not record one, the contract says
so.

| Capture | Spike log | Section | What it is |
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

The spike also recorded `jq` projections of some replies, not full envelopes. They cannot be
replayed, so they live in [docs/evidence/](../docs/evidence/README.md) with the desktop app's log
excerpts.

Captures added after the spike come from `scripts/capture-unity.sh`, run during the manual-gate
check that the "Source" column names, with the CLI version that produced them. A row marked
**pending** is a capture that check has not added yet: whoever runs it names the files it wrote and
replaces **pending** with the version and the date.

| Capture | Source | Version | What it is |
|---|---|---|---|
| `version.raw.txt`, `version.exit` | [M0 gate](../docs/manual-gates/M0.md), "Capture the M1 replies" | 1.0.0-beta.13, 2026-10-19 | `unity --version`: the version `doctor` checks against 1.0.0-beta.13 |
| `cache-key.json`, `cache-key.exit` | [M0 gate](../docs/manual-gates/M0.md), "Capture the M1 replies" | 1.0.0-beta.13, 2026-10-19 | `unity cache key <project> --format json` from inside the project, the path positional, so it is `${cwd}`: the key the staleness checks compare across checkouts (contracts (g) step 7, A20) |
| `license-status.json`, `license-status.exit` | [M3 gate](../docs/manual-gates/M3.md), "Capture the M3 replies", step 2 | 1.0.0-beta.13, 2026-10-21 | `unity license status`, exit 0: `active`, `signedIn`, `authMode` and the licences held (Unity Personal, Asset Store). What `doctor` reports (#93). Its `machineId`, which identifies the Mac, was replaced with `<machine-id>` before the commit, the one change to the CLI's bytes |
| `projects-verify-pass.json`, `projects-verify-pass.exit` | same, step 2 | 1.0.0-beta.13, 2026-10-21 | `unity projects verify --strict <slot>` on a clean slot, exit 0: no errors or warnings in 12,587 files (#87; #29) |
| `projects-verify-fail.json`, `projects-verify-fail.exit` | same, step 2 | 1.0.0-beta.13, 2026-10-21 | the same on a slot with `Assets/AddressableAssetsData.meta` moved out, exit 6: `PROJECTS_VERIFY_FAILED`, one `META_MISSING` error naming the folder, with a hint (#87; #29) |
| `cache-key-project-path-rejected.json`, `cache-key-project-path-rejected.exit` | same, step 2 | 1.0.0-beta.13, 2026-10-21 | `unity cache key --project-path <repo>`, exit 2: `INVALID_COMMAND_ARGS`, the exit code `cache-key-project-path-invalid.json` lacks (#12) |
| `build-not-a-unity-project.json`, `build-not-a-unity-project.exit` | same, step 2 | 1.0.0-beta.13, 2026-10-21 | `unity build --list-profiles` from `/tmp`, exit 6: `COMMAND_FAILED`, "Not a Unity project (ProjectVersion.txt not found at ${cwd})." (#12) |
| `status-no-instances.json`, `status-no-instances.exit` | same, step 2 | 1.0.0-beta.13, 2026-10-21 | `unity status` with `UNITY_PROJECT_PATH` naming a project with no Editor, without `--until-ready`, exit 6: `STATUS_NO_INSTANCES` (#12) |
| `vcs-doctor-fix.json`, `vcs-doctor-fix.exit` | same, step 3 | 1.0.0-beta.13, 2026-10-21 | `unity vcs doctor --fix` on a throwaway clone of the project, exit 4: `VCS_DOCTOR_FAILED`, 5 findings, 3 repaired. It added 27 LFS patterns to `.gitattributes`, after which git showed 717 tracked binary files as modified; it added 49 rules to `.gitignore`; and it installed the LFS hooks `post-checkout`, `post-commit`, `post-merge` and `pre-push` in `.git/hooks`. It left a package dependency on a git branch (warning) and one on an unverified revision (notice) (#88; #30) |
| `vcs-merge-setup.json`, `vcs-merge-setup.exit` | same, step 3 | 1.0.0-beta.13, 2026-10-21 | `unity vcs merge-setup` on the same clone, exit 0: the Editor's UnityYAMLMerge as `merge.unityyamlmerge.driver` and `merge.tool` in `.git/config`, and merge attributes for Unity's YAML types (#88; #30) |
| `vcs-doctor.json`, `vcs-doctor.exit` | [M3 gate](../docs/manual-gates/M3.md), "VCS set-up", step 2 | 1.0.0-beta.13, 2026-10-21 | `unity vcs doctor <repo> --format json`, check-only, on a throwaway clone of the project, run from it, exit 4: `VCS_DOCTOR_FAILED`, "4 problem(s) need attention.", the same 5 findings as `vcs-doctor-fix.json`, `fixed: false` and nothing repaired; the clone's files, hooks and git configuration were left unchanged. What `doctor` reports (#88; A35). Recorded for #88's pull request with the CLI alone, no Editor |
| `open-headless.json`, `open-headless.stderr.txt`, `open-headless.exit` | same, step 4 | 1.0.0-beta.13, 2026-10-21 | `unity open <slot> --args "-batchmode"`, exit 0: `launchedPid`, the Editor version and path, and the Hub-registry warning on stderr and in `warnings[]`. It was `ready` 21 s later (A33; #89) |
| `open-gui-streams.json`, `open-gui-streams.stderr.txt`, `open-gui-streams.exit` | same, step 4 | 1.0.0-beta.13, 2026-10-21 | `unity open <slot>` for a GUI Editor on a path the Hub does not know, exit 0. The `Warning: … opening as a file path.` line is on **stderr**; stdout is bare JSON that repeats it in `warnings[]`. It was `ready` 20 s later (#17) |
| `command-eval-first-call.json`, `command-eval-first-call.exit` | same, step 4 | 1.0.0-beta.13, 2026-10-21 | the first `eval` on that GUI Editor, just after it was `ready`, exit 0: the result at once. The spike's first-call timeout did not recur (#12) |
| `command-eval-ambiguous-editor.json`, `command-eval-ambiguous-editor.exit` | same, step 4 | 1.0.0-beta.13, 2026-10-21 | `eval` from `/tmp` with two Editors up and no `UNITY_PROJECT_PATH`, exit 6: `AMBIGUOUS_EDITOR`, listing both slots and their ports (#12) |
| `close-unreachable.json`, `close-unreachable.exit` | [M4 gate](../docs/manual-gates/M4.md), check 2 | 1.0.0-beta.13, Editor 6000.4.11f1, 2026-10-23 | `unity close <slot> --format json` on a headless Editor 30 s into a cold import of the spike's project, its Library deleted: exit 6, `PROJECTS_CLOSE_UNREACHABLE` ("running … but has no graceful-close channel. Re-run with --force to terminate it."), `closed: false`, `method: none`, `reason: unreachable`; the Editor still running. What A40's close of a re-warm Editor that is not ready used to run (#224). Recorded by the agent on the maintainer's Mac, at the maintainer's request |
| `close-force-unreachable.json`, `close-force-unreachable.exit` | same | 1.0.0-beta.13, Editor 6000.4.11f1, 2026-10-23 | `unity close <slot> --force --format json` on the same Editor, a second later: exit 0, `closed: true`, `method: forced`; returned in 1 s, the process gone 1 s later. What refresh runs on an Editor that is not ready (A40; #224) |

### captures/claude/

Claude Code's own output, other than hook stdin: stdout as printed, with paths replaced by
`scripts/scrubpaths`, and the exit code in a `.exit` file beside it. These were taken after the
spike, so each row names its source as the table above does, and a **pending** row works the same
way.

| Capture | Source | Version | What it is |
|---|---|---|---|
| `version.txt`, `version.exit` | [M0 gate](../docs/manual-gates/M0.md), "Capture the M1 replies" | 2.1.299, 2026-10-19 | `claude --version`, from the desktop app's bundled binary, there being no CLI on `PATH`: the version `doctor` checks against 2.1.295 |

### captures/git/

Git output that the release and remove rules are built on. Tests do not parse these files: they
build the same states with real git in a temporary repository (an untracked file; a
`scriptingDefineSymbols` line with the same set in a different order) and run upool against that.
The captures are the reference for what those states look like.

| Capture | Spike log | Section | What it is |
|---|---|---|---|
| `projectsettings-define-reorder.diff` | `d3-clone1-projectsettings.diff` | §8 D.3; §9 G | the only observed define-order churn: `scriptingDefineSymbols` reordered, same set; the noise rule reverts exactly this |
| `remove-status-untracked.txt` | `test-a/remove-status-1791774813.txt` | §8 A.8 | `git status --porcelain` taken by the remove hook after session 3: `?? spike-reuse.txt`; the slot must be parked |

### Captured but not copied

Kept only in the spike's `logs/` folder, because nothing in upool parses them: the Unity Editor logs
(`*-run*.log`, `cold*.log`, `conc*.log`, `mem-*.log`, `b-headless-*.log`, `headless.log`,
`churn-batch.log`, `g-repro-headless.log`, `main-after.log`, `wtclone-run*.log`), `commands.log`,
`measurements.txt`, `setup.txt`, the `--help` texts, the probe hook scripts and their own logs
(`fired.log`, `probe.log`, `git.log`, `selftest.log`), the desktop app's `git-worktrees.json`
snapshots (`e4-*`, `test-e/registry/`, `test-gc/`), `symlink-check.tsv`, `d4-settings-diff.txt`,
`c5-startup-scene.diff`, `t6-test-results.xml`, `t6-dryrun-human.txt`, `t7-claude-p.*`, the
`unity projects` listings (`projects-before.json`, `projects-after-seed-cache.json`), which name
every project in the capturing machine's Unity Hub, the `unity vcs git worktree` envelopes
(`t6-dryrun.json`, `t6-seed-*.json`, `t6-remove-seed-*.json`), because upool never wraps that
command and their home-redacted paths fit no variable, and the
`jq` projections of successful calls (`b-targeting.txt` B2a–B2g except B2c and B2f, B3d;
`t6-targeting.txt` D, D2, F, F2, G; `b5-close.txt`). The spike report quotes what matters from them.

## golden/

upool's own expected output: `--json` envelopes, human-readable `status`, hook stdout. Unlike a
capture, a golden file changes whenever upool's output changes on purpose. Each package that
compares against golden files defines an `-update` flag, so regenerate them with
`go test ./<package> -update` (`./cmd/upool` for a command's output), read the diff, and commit it
with the change that caused it. Golden files use the same path variables as captures, which keeps
`make check-paths` passing.
