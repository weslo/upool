# Warm-Library worktree pool — macOS spike report

Project: `down` (Unity 6000.4.11f1), 2026-10-11. All raw logs are in `logs/` beside this file:
`commands.log` holds every command with its wall time, `measurements.txt` holds the free-space
samples, and each Editor run has its own log named after the run.

## 1. Environment

| Item | Value |
|---|---|
| macOS | 15.7.3 (24G419), arm64, Apple M1 Max (10 cores: 8P + 2E) |
| RAM | 64 GB (68,719,476,736 bytes) |
| Volume | `Data` (`/dev/disk3s5`), APFS; `$SPIKE` is on the same device as `$ROOT` (cloning possible) |
| Free space at start | 492 GB (needed: 2 × 6.5 GB + 5 GB = 18 GB) |
| Unity CLI | 1.0.0-beta.12 at start, below the required beta.13; upgraded with `unity self-update` to **1.0.0-beta.13** before any test |
| Claude Code | not on `PATH`; used the desktop app's bundled binary, 2.1.295 (`~/Library/Application Support/Claude/claude-code/2.1.295/…/claude.app/Contents/MacOS/claude`) |
| git / jq | 2.55.0 / jq-1.7.1-apple |
| `$PROJ` | the repo root (`$SUB` empty); Editor 6000.4.11f1 (b0a1d6caadd2) |
| `$UNITY` | `/Applications/Unity/Hub/Editor/6000.4.11f1/Unity.app/Contents/MacOS/Unity` |
| Pipeline package | `com.unity.pipeline` 0.7.0-exp.1 already in `manifest.json` (no install needed) |
| `.gitignore` covers `Library/` | yes |
| Repo checkout | 7,155 tracked files, no LFS; ~3,400 source assets (2,185 WAV, 472 prefab, 336 FBX, 181 .cs, 18 shaders) |

**Library (warm, Editor closed):** `du -sh` 6.5 GB, apparent (`-A`) 6.3 GB, 90,959 files (90,962 while
the Editor was open). Largest subfolders:

| Folder | Size |
|---|---|
| PackageCache | 2.3 GB |
| BuildCache | 1.7 GB |
| Artifacts | 1.1 GB |
| Bee | 637 MB |
| BurstCache | 374 MB |
| ArtifactDB | 97 MB |
| ShaderCache | 92 MB |
| PlayerDataCache | 84 MB |

The source Library was **stale relative to HEAD**. #237's `.cs` files were written to the checkout at
22:44 on Oct 10, but `ScriptAssemblies` dated from 21:58. The Editor stayed open overnight and never
refreshed. This affected Tests 1–3; see Test 3b.

Before starting, an Editor (PID 23858, opened from the Hub with `-automated`) had the project open.
You closed it. No other Claude sessions were running in this checkout.

## 2. Results by step

| Step | Measured | Assumption tested | Verdict |
|---|---|---|---|
| Setup 1 | APFS, same device | clone is possible | confirmed |
| Setup 2 | CLI beta.12 → beta.13; `claude` not on PATH | tooling present | contradicted, then fixed (upgrade; bundled binary) |
| T1.3 clone | `cp -cR` 15.0 s | clone costs seconds | **confirmed** |
| T1.4 free-space delta | −8 MB (free space went *up*; noise) | clone costs no disk | **confirmed** (<0.01 % of 6.3 GB) |
| T1.5 instance files | `EditorInstance.json`, `ProtocolInstance.json` absent; `Pipeline/` present (1 file, `eval-usage.jsonl`) | Editor closed cleanly | confirmed; `Pipeline/` survives a clean exit |
| T1.6 file count | 90,959 cloned = source; 90,958 after stripping `Pipeline/` | clone is complete | confirmed |
| T1 (extra) worktree checkout | **~1,435 MB real disk per worktree** (every one of 7 created) | — | new: the checkout, not the Library, is the per-slot disk cost |
| T2 run 1 | 40 s, 10 imports, DAG rebuild, 7 project assemblies recompiled | zero reimports, one recompile | recompile **confirmed**; the 10 imports came from the stale source (see T3b), not from cloning |
| T2 run 2 | 10 s, 1 import, no compile | second run fully warm | **confirmed** |
| T2 run 3 (1 `.cs` edit) | 11 s, 1 import, 1 assembly (Down.Runtime) | incremental edits are cheap | confirmed |
| T2 F2→F3 | 334 MB | first-run divergence | measured (Bee 230 MB, ArtifactDB 97 MB, SourceAssetDB 35 MB rewritten) |
| T2 F3→F4 / F4→F5 | −41 MB / −7 MB (free space rose) | per-run divergence after warm | ~0 per warm run |
| T3.1 clone2 | 14.8 s, +24 MB | second slot costs the same | **confirmed** |
| T3.2 clone2 run 1 | 39 s, 10 imports, 7 assemblies, 337 MB | same as clone1 | confirmed |
| T3.3 main-after | 16 s, DAG rebuild, 2 assemblies, 1 prefab import | source unharmed (zero imports) | **contradicted, but not caused by cloning.** Main was already stale (above); clones never write to the source. Main recompiled exactly the 2 assemblies #237 touched. |
| T3b (added) clone3 from the now-fresh main | clone 14.6 s, +15 MB; run 1 **38 s, 1 import** (EditorBuildSettings only), DAG rebuild, 7 assemblies, 321 MB | clone of a *fresh* Library gives zero reimports | **confirmed** |
| T4 cold run 1 | **235 s**, 12,736 imports, 375 assemblies, Library grew 3.8 GB; peak Editor RSS 4.5 GB, 6.8 GB total, 10 shader compilers; packages resolved in 16 s | clone saves most of the cold cost | **confirmed**: 38 s first run vs 235 s (6×). Cold is far cheaper here than on Windows. |
| T4 cold run 2 | 10 s, 1 import | — | warm |
| T5 2 warm, concurrent | 11 s, 13 s (solo 10–13 s); peak RSS 1,447 / 1,338 MB; 2 shader compilers; 4.1 GB total | two warm Editors run together | **confirmed** |
| T5 3 warm, concurrent | 12, 12, 14 s; peak RSS ~1.43 GB each; 3 shader compilers; 5.9 GB total | three fit on ≥32 GB | **confirmed** |
| T5.5 2 cold, concurrent | 259 s, 258 s (solo 235 s, +10 %); peak RSS 4.2 GB each; **20 shader compilers**; 12.5 GB total; both exited 0 | cold imports must be serialized (crashed 3/4 on Windows 15 GB) | **contradicted on this machine**: no failure, small slowdown |
| T6.1 `worktree add/remove --help` | exist; `--seed`, `--no-register`, `--install-editor`, `--into`, `--dry-run`, `-y` all present (verbatim below) | CLI has worktree + `--seed` | **confirmed** |
| T6.2 dry-run | plan JSON; `--into` is the worktree dir itself; branch is required (`branchKind: new`); no detached mode | — | confirmed |
| T6.3 `--seed cache` | 14.4 s (seeding 6.7 s), 39,930 files / 4.3 GB cloned, +1,458 MB (= checkout only); **`PackageCache/` withheld**; `Artifacts/`, `ScriptAssemblies/`, `Bee/` and `Pipeline/` present; Hub registered | seed is a clone | **confirmed** clone. But its first run took **51 s and 2.7 GB of real disk** (PackageCache re-extracted, not cloned) |
| T6.3 `--seed full` | 18.9 s (seeding 14.2 s), 90,858 files / 6.65 GB cloned, +1,452 MB; first run **36 s, 322 MB** | — | full is strictly better than cache on APFS |
| T6.3 remove | `unity vcs git worktree remove -y`: removed, `unregistered: true`, `branchKept: true` | — | works; branches must be deleted separately |
| T6.4 batch Editor in `unity status` | listed, `state: ready`, port 7800 | sources disagree | **listed** |
| T6.4 env var `status --until-ready` | `ready` at once (0 s) | reaches ready | confirmed |
| T6.4 env var `recompile` | `up_to_date`, 1 s, targeted clone1 | targets that Editor | confirmed |
| T6.4 env var `command` | 151 commands; `target.projectPath` = clone1 | lists commands | confirmed |
| T6.4 env var for a project with no Editor (added) | `COMMAND_FAILED: No Pipeline instance found for project …clone2` | variable is honoured, not a hint | **confirmed** (fails rather than falling back) |
| T6.4 cwd discovery, no env var | from `/tmp` **and from inside clone2 (no Editor)**, `unity command eval` ran in **clone1's** Editor | cwd selects the project | **contradicted**: with one Editor up, cwd is ignored and the call lands in the wrong slot |
| T6.4 `unity close` on a batch Editor | `closed: true`, `method: graceful`, 2 s | close applies to batch Editors | confirmed |
| T6.5 non-existent path, `--until-ready --timeout 15` | waits the full **15.1 s**, then `STATUS_NO_INSTANCES`, **exit code 6** (same for an existing project with no Editor) | waits rather than failing at once | confirmed (waits) |
| T6.6 `UNITY_PROJECT_PATH=… unity test` from `/tmp` | fails at once: `Not a Unity project (ProjectVersion.txt not found at /private/tmp)`, exit 6 | test resolves the project through the variable | **contradicted**: `unity test` ignores the variable |
| T6.6 (added) `unity test <path> --mode EditMode` | 70 s total, own batch Editor, 3/3 passed | — | positional path works |
| T7.4 `claude -p --worktree probe` (clean env) | `WorktreeCreate` fired, `cc-probe` created, session started **in `cc-probe`**, then `Not logged in · Please run /login` (exit 1) | sibling worktree accepted | **confirmed**; no `Refusing to use` / `Error creating worktree` |
| T7.5 create-hook stdin | `session_id`, `transcript_path`, `cwd`, `hook_event_name`, `name` (verbatim below) | `name` + `session_id` on stdin | **confirmed** (`worktree_name` absent) |
| T7.5 `CLAUDE_ENV_FILE` | set for SessionStart (`~/.claude/session-env/<sid>/sessionstart-hook-0.sh`) and the export was written | exports reach Bash | **confirmed** in the desktop follow-up (§7): `UPOOL_PROBE=spike-ok` in Bash, both with and without a worktree |
| T7.5 WorktreeRemove for `-p` | did not fire; removed `cc-probe` by hand | — | as expected |
| T7.6 desktop app | **fires `WorktreeCreate`** from the app's own main process (`Creating worktree for session … from …/down`), but the hook died: `getcwd: cannot access parent directories: Operation not permitted` and `/bin/bash: …/wt-create.sh: Operation not permitted`; app aborted with `worktree_hook_failed` | desktop app fires the hook and the pool can serve it | **contradicted as deployed**: hook fires, but the spawning process can't reach `~/Documents` (TCC-like denial). No `create-input.json`, no worktree. Follow-up probe in §7 |
| Follow-up: gitignored agent files in a `git worktree add` slot | `AGENTS.md`, `CLAUDE.md`, `.claude/`, `.agents/` all **absent** (gitignored here) | — | new: a session in a git-checkout slot loads none of the project's agent guidance or local settings |
| Follow-up TA: whole-checkout clone (`git worktree add --no-checkout`, then `cp -cR` of every top-level entry except `.git`, `Temp`, `Logs`, `obj`, `Builds`, then `git reset -q`) | 25.9 s total (clone 18.2 s, index 7.7 s); **47 MB** of disk against 1,435 MB for a checkout; `git status` clean; mtimes preserved; agent files present. First run **37 s, 1 import, 0 assemblies compiled**, DAG still rebuilt, 331 MB divergence; second run 9 s | cloning the working tree removes the checkout cost and the recompile | **confirmed** for disk, recompile and agent files. **Not** for first-open time: the path-driven Bee rebuild still costs ~23 s |
| Follow-up TB: desktop, worktree checked, hook in `~/.upool-probe/` | `WorktreeCreate` fired (parent `Claude.app/Contents/Helpers/disclaimer`, grandparent `Claude.app/Contents/MacOS/Claude`); worktree made at `~/.upool-probe/cc-shell-command-output-8ff578` in ~4.6 s; session ran there; reply `pwd` = that path, `UPOOL_PROBE=spike-ok` | desktop app fires the hook and accepts a sibling worktree | **confirmed** |
| TB: hook's access to `~/Documents` | `getcwd` EPERM; `head …/ProjectVersion.txt` EPERM; `ls -d` OK; **`git -C … worktree add` OK** | hook inherits the app's file access | **contradicted**: disclaimed process; plain tools can't read `~/Documents`. Why `git` could is unexplained (CLT git via `/usr/bin/git`) |
| TB: create-hook stdin | same fields as the CLI, but `transcript_path` is `""`, `name` is generated from the first prompt plus a hex suffix, and `session_id` is the **app's** session id (`local_<id>`), not the CLI's | `session_id` identifies the session | **contradicted** for correlation: create/remove `session_id` ≠ `SessionStart` `session_id` |
| TB: config overlay / trust | app log: "Skipped per-machine config overlay" and "Skipped auto-trust for hook-based worktree — hook came from a repo-committed settings tier …" | — | new: the app doesn't copy local config into hook-made worktrees registered from project settings. A user-level hook might count as "vouched" (untested) |
| TB: `WorktreeRemove` on archive | fired with `worktree_path`; the hook removed the worktree; app logged "Removed hook-based worktree" | release hook exists | **confirmed** |
| TB: desktop, worktree unchecked | `SessionStart` fired in the CLI process (`getcwd` OK in `~/Documents`); reply `pwd` = repo, `UPOOL_PROBE=spike-ok` | env exports reach Bash | **confirmed** |
| TB: `SessionStart` hook cwd | process cwd was the **repo**, even when stdin `cwd` was the worktree | — | new: use stdin `cwd`, not `$PWD`, inside hooks |

## 3. Timings side by side

| Run | Wall | Imports (`Start importing`) | DAG rebuild | Assemblies compiled |
|---|---|---|---|---|
| cold run 1 | **235 s** | 12,736 | (Library rebuilt) | 375 |
| cold run 2 | 10 s | 1 | no | 0 |
| clone1 run 1 (stale source) | 40 s | 10 | yes | 7 |
| clone1 run 2 | **10 s** | 1 | no | 0 |
| clone1 run 3 (one `.cs` edit) | 11 s | 1 | no | 1 |
| clone2 run 1 (stale source) | 39 s | 10 | yes | 7 |
| clone3 run 1 (fresh source) | **38 s** | 1 | yes | 7 |
| main-after (stale main) | 16 s | 2 | yes | 2 |
| `--seed cache` run 1 | 51 s | 1 | yes | 7 (+ 16 s package resolve) |
| `--seed full` run 1 | 36 s | 1 | yes | 7 (3 s package resolve) |
| 2 warm concurrent | 11 s / 13 s | 1 / 1 | no | 0 |
| 3 warm concurrent | 12 / 12 / 14 s | 1 each | no | 0 |
| 2 cold concurrent | 259 s / 258 s | 12,736 each | — | 375 each |
| `unity test` EditMode (warm clone) | 70 s | — | — | — |
| whole-checkout clone run 1 (TA) | 37 s | 1 | yes | **0** |
| whole-checkout clone run 2 (TA) | 9 s | 1 | no | 0 |

The 1 import in every warm run is `ProjectSettings/EditorBuildSettings.asset`, which batch mode
reimports each time. The 10 imports on clones of the stale source were 9 prefabs plus that file.

**Why the first open at a new slot path is slow (~25 s extra):** there are two separate causes.
- **Path (unavoidable).** `Library/Bee/200b0aEDbg-inputdata.json` embeds the absolute project path
  (191 occurrences), so any new path rebuilds the DAG. That is a 10 s Tundra pass plus a second pass
  that updates ~560 path-dependent items without running the compiler. It reports ~23 s of
  "compile time" either way: clone3 24.3 s with 7 Csc, TA 23.0 s with 0 Csc.
- **mtimes (avoidable).** `git worktree add` writes `Assets/` with fresh mtimes, so the 7 project
  assemblies recompile too. Cloning the working tree keeps mtimes and removes that, but it saves
  little time.

Only a persistent slot avoids the path cost.

## 4. Free-space deltas

| What | Cost |
|---|---|
| `cp -cR Library` (clone1 / clone2 / clone3) | −8 / +24 / +15 MB, i.e. ≈0 |
| `git worktree add` checkout, per slot | **~1,435 MB** (consistent across 7 worktrees) |
| First run divergence (clone1 / clone2 / clone3) | 334 / 337 / 321 MB |
| Warm run divergence | ≈0 (−41, −7 MB: free space rose) |
| `--seed cache`: create / first run | 1,458 MB (checkout) / **2,707 MB** |
| `--seed full`: create / first run | 1,452 MB (checkout) / 322 MB |
| Cold Library, one slot | 3,785 MB (3.5 GB apparent, 66,313 files; no BuildCache) |
| Two cold slots | 7,490 MB |
| Whole-checkout clone (TA): create / first run | **47 MB** / 331 MB |

A persistent slot costs about 1.8 GB with a git checkout (checkout plus first-run divergence), or about
0.4 GB with a whole-checkout clone (47 MB plus 331 MB divergence). At 492 GB free, disk is not what
limits slot count.

## 5. Verbatim output

### `unity vcs git worktree add --help` (CLI 1.0.0-beta.13)

```
Usage: unity vcs git worktree add [options] <branch> [path]

Create a worktree for a branch, seed its Library from this one, and register it
with the Hub

Arguments:
  branch             The branch to check out. Created from the current HEAD if
                     it does not exist yet.
  path               Project path or name to branch from (defaults to the
                     current directory)

Options:
  --into <dir>       Where to create the worktree (defaults to a folder beside
                     the repository, named after the branch)
  --seed <mode>      How much of this checkout's Library to copy: cache, full,
                     or none
  --install-editor   Install the editor the branch needs, rather than only
                     reporting it
  --no-register      Leave the new worktree out of the Hub project registry
  --dry-run          Run every check and report the plan, then change nothing
  -y, --yes          Answer yes to every confirmation
  -h, --help         display help for command
```

(Global options omitted; full text in `logs/help-worktree-add.txt`.)

### `unity vcs git worktree remove --help`

```
Usage: unity vcs git worktree remove [options] <path>

Delete a worktree and drop its row from the Hub project registry

Arguments:
  path               The worktree to remove. Required, so a bare run never
                     deletes the checkout you are in.

Options:
  --discard-changes  Delete the worktree even though it holds uncommitted work
  --force            Remove even while an Editor holds the worktree
  --dry-run          Run every check and report the plan, then change nothing
  -y, --yes          Answer yes to every confirmation
  -h, --help         display help for command
```

The `--seed cache` plan withholds two groups. Identity files withheld:
`ArtifactDB-lock`, `InspectorExpandedItems.asset`, `LastSceneManagerSetup.txt`,
`PreviousActionData.bin`, `PreviousActionStack.bin`, `SceneVisibilityState.asset`,
`SourceAssetDB-lock`, `expandedItems`, `ilpp.pid`. Content-addressed files withheld: `PackageCache`.
JSON output from `worktree add`/`remove` **redacts the home directory** (`/Users/<user>/…`), so a tool
can't use those paths verbatim.

### `WorktreeCreate` stdin (`claude -p --worktree probe`, CLI 2.1.295)

```json
{
  "session_id": "3965aa22-ae6d-4000-9c6e-c5d6f22043c1",
  "transcript_path": "/Users/wes/.claude/projects/-Users-wes-Documents-Personal-down/3965aa22-ae6d-4000-9c6e-c5d6f22043c1.jsonl",
  "cwd": "/Users/wes/Documents/Personal/down",
  "hook_event_name": "WorktreeCreate",
  "name": "probe"
}
```

### `SessionStart` stdin (same session)

```json
{
  "session_id": "3965aa22-ae6d-4000-9c6e-c5d6f22043c1",
  "transcript_path": "/Users/wes/.claude/projects/-Users-wes-Documents-Personal--upool-spike-down-cc-probe/3965aa22-ae6d-4000-9c6e-c5d6f22043c1.jsonl",
  "cwd": "/Users/wes/Documents/Personal/.upool-spike/down/cc-probe",
  "hook_event_name": "SessionStart",
  "source": "startup"
}
```

The session's project key moved to the slot path. Transcripts are keyed by the slot path. (Corrected in §9: auto-memory follows the repo, not the slot.)

### Desktop app (`~/Library/Logs/Claude/main.log`, app 2.31226.1)

```
2026-10-11 20:54:20 [info] Creating worktree for session local_ba750fc3-f573-4cfe-ab18-2f0b63f81583 from /Users/wes/Documents/Personal/down
2026-10-11 20:54:20 [error] WorktreeCreate hook failed: /Users/wes/Documents/Personal/.upool-spike/down/hooks/wt-create.sh: shell-init: error retrieving current directory: getcwd: cannot access parent directories: Operation not permitted
/bin/bash: /Users/wes/Documents/Personal/.upool-spike/down/hooks/wt-create.sh: Operation not permitted
2026-10-11 20:54:20 [error] Worktree creation failed with error: worktree_hook_failed, aborting session { baseRepo: '/Users/wes/Documents/Personal/down', sessionId: 'local_ba750fc3-f573-4cfe-ab18-2f0b63f81583' }
```

### Desktop follow-up: `WorktreeCreate` stdin (hook in `~/.upool-probe/`)

```json
{
  "session_id": "d003da2d-8318-401e-9e04-5333aacbf7c8",
  "transcript_path": "",
  "cwd": "/Users/wes/Documents/Personal/down",
  "hook_event_name": "WorktreeCreate",
  "name": "shell-command-output-8ff578"
}
```

### Desktop follow-up: `SessionStart` stdin (worktree session)

```json
{
  "session_id": "65d40ae0-886a-45de-ba41-a8beaf3c1f5b",
  "transcript_path": "/Users/wes/.claude/projects/-Users-wes--upool-probe-cc-shell-command-output-8ff578/65d40ae0-886a-45de-ba41-a8beaf3c1f5b.jsonl",
  "cwd": "/Users/wes/.upool-probe/cc-shell-command-output-8ff578",
  "scratchpad_dir": "/private/tmp/claude-501/-Users-wes--upool-probe-cc-shell-command-output-8ff578/65d40ae0-886a-45de-ba41-a8beaf3c1f5b/scratchpad",
  "hook_event_name": "SessionStart",
  "source": "startup"
}
```

### Desktop follow-up: `WorktreeRemove` stdin (on archive)

```json
{
  "session_id": "d003da2d-8318-401e-9e04-5333aacbf7c8",
  "transcript_path": "",
  "cwd": "/Users/wes/Documents/Personal/down",
  "hook_event_name": "WorktreeRemove",
  "worktree_path": "/Users/wes/.upool-probe/cc-shell-command-output-8ff578"
}
```

### Desktop follow-up: app log (`main.log`)

```
21:24:46 Creating worktree for session local_d003da2d-8318-401e-9e04-5333aacbf7c8 from /Users/wes/Documents/Personal/down
21:24:51 [WorktreeHooks] WorktreeCreate hook created worktree at /Users/wes/.upool-probe/cc-shell-command-output-8ff578
21:24:51 Skipped per-machine config overlay for hook-based worktree "shell-command-output-8ff578" — hook came from a repo-committed settings tier, or its path was selected from multi-line output (provenance not vouched)
21:24:51 Created hook-based worktree "shell-command-output-8ff578" at /Users/wes/.upool-probe/cc-shell-command-output-8ff578
21:24:51 Using worktree "shell-command-output-8ff578" at /Users/wes/.upool-probe/cc-shell-command-output-8ff578 for session local_d003da2d-8318-401e-9e04-5333aacbf7c8
21:24:51 Skipped auto-trust for hook-based worktree at /Users/wes/.upool-probe/cc-shell-command-output-8ff578 — hook came from a repo-committed settings tier, or its path was selected from multi-line output (provenance not vouched)
21:24:54 [CCD start-timing] … worktree=4634ms … total_to_assistant=7542ms … worktree=true
21:26:17 Removing worktree "shell-command-output-8ff578" (leased by local_d003da2d-…): /Users/wes/.upool-probe/cc-shell-command-output-8ff578
21:26:18 Removed hook-based worktree "shell-command-output-8ff578"
```

Full probe log, hook scripts and stdin files: `logs/desktop-probe/`.

## 6. What this changes in the design

| Default | Call | Why |
|---|---|---|
| Slots persistent, seeded by clone | **Keep** | Clone: 15 s, ≈0 disk. First open at a new path: 38 s versus 235 s cold. Persistence saves a further ~28 s and ~330 MB per reuse; it helps, but matters less than on Windows. Two additions. (1) Seed only from a Library known to be current at HEAD. Main's Editor-held Library was stale, and clones inherit staleness: 9 extra imports. Run a 16 s batch refresh on the source first, or seed from a warm slot. (2) Create slots by cloning the **whole checkout**: `git worktree add --no-checkout --detach`, then `cp -cR` of every top-level entry except `.git`/`Temp`/`Logs`/`obj`/`Builds`, then `git reset -q` (TA). That takes 26 s and 47 MB instead of 1.4 GB, leaves a clean status, needs no recompile, and carries the gitignored agent files a git checkout loses. If you use the CLI instead, take `--seed full`, never `--seed cache`: withholding `PackageCache` cost 15 s and 2.7 GB per slot. (3) Persistence is still worth it: the path-driven Bee rebuild costs ~23 s on every first open at a new path, whichever way the slot is made. |
| `max_slots = 3` | **Keep as default**; don't derive it from disk | About 1.8 GB per slot. Disk allows dozens; the limit is Editors and RAM. |
| `max_editors = 2` | **Change** to RAM-derived, e.g. 3 at 64 GB | Three warm Editors ran at ~1.4 GB each with no slowdown. Caveat: these were short `-batchmode -quit` runs. A long-lived GUI Editor (the one you closed had 10 shader compilers) will use more, so confirm against interactive Editors before going past 3. |
| `max_cold_imports = 1` | **Keep** (low priority) | Two concurrent cold imports were safe here: +10 % time, 12.5 GB, 20 shader compilers. The Windows crash didn't reproduce. A cold import is 4 min and clone seeding makes it rare, so serializing costs little. On ≥32 GB Macs it could be 2. Don't gate on `memory_pressure -Q` free %: it read 92–93 % throughout, even at 12.5 GB of Editor RSS. |
| Editors targeted through `UNITY_PROJECT_PATH` | **Keep, and make it mandatory**, plus pass the path explicitly | Without the variable, a call from inside slot 2 silently ran in slot 1's Editor. With it, wrong targets fail loudly. **`unity test` ignores the variable**: pass the project positionally (and audit `build`/`run`). `--until-ready` waits out its timeout and then exits 6, so it works as a wait-for-boot primitive. |
| Claude Code through `WorktreeCreate` | **Keep, with constraints** (see §7) | Works from both the CLI and the desktop app. The desktop app accepted a hook-made worktree outside the repo and outside `~/Documents`, ran the session there, delivered the `CLAUDE_ENV_FILE` export to Bash, and fired `WorktreeRemove` on archive. Constraints: (1) the hook script must live **outside** TCC-protected folders. The desktop runs it through `Claude.app/Contents/Helpers/disclaimer`, so it does not inherit the app's Documents access: `bash` and `head` got EPERM inside `~/Documents` (`git` worked; reason unexplained). So the hook should only hand out a pre-made slot, not copy from `~/Documents`, and slots should live outside protected folders (or the repo should). (2) Correlate by worktree path, not `session_id`: on the desktop, the create/remove `session_id` is the app's id, not the CLI's `SessionStart` id. (3) The hook must restore the gitignored agent files (`AGENTS.md`, `CLAUDE.md`, `.claude/`, `.agents/`). The app skips its own config overlay for hooks from project settings, and a git checkout loses them; the whole-checkout clone keeps them. (4) Transcripts are keyed by slot path; auto-memory follows the repo (§9). |

## 7. Follow-up: was the desktop failure a blocker?

**No, for the approach; yes, for one deployment detail.** The first desktop attempt failed only because
the hook script lived under `~/Documents`. The desktop app runs `WorktreeCreate` through
`Claude.app/Contents/Helpers/disclaimer`, so the hook process does not inherit Claude.app's Documents
access, and bash couldn't open the script. With the script in `~/.upool-probe/`, the whole desktop flow
worked: the hook made a sibling worktree, the app ran the session in it, `CLAUDE_ENV_FILE` exports
reached Bash, and archiving fired `WorktreeRemove`.

What the pool tool has to do as a result:

1. **Put the hook script outside TCC-protected folders** (`~/Documents`, `~/Desktop`, `~/Downloads`).
2. **Keep the hook trivial.** Hand out a pre-made persistent slot and print its path. Don't clone or read
   from a repo under `~/Documents` inside the hook: `head` got EPERM. `git` happened to work, but that's
   unexplained, so don't rely on it. Do the cloning in the tool's own process (run from Terminal or
   launchd with access), ahead of time. The cleanest option is a repo and pool outside protected folders.
3. **Correlate by worktree path.** The desktop's create/remove `session_id` is the app's id; the
   CLI's `SessionStart` carries a different id.
4. **Use `name` as a hint only.** The desktop generates it from the first prompt (e.g.
   `shell-command-output-8ff578`).
5. **Restore the gitignored agent files in each slot.** The app skipped its own config overlay for this
   hook ("provenance not vouched"). The whole-checkout clone carries them; a git checkout does not.
6. **Inside hooks, use stdin `cwd`, not `$PWD`.** The `SessionStart` process ran in the repo even for a
   worktree session.
7. **Untested:** registering the hook in user-level settings (`~/.claude/settings.json`) may count as
   vouched and enable the app's overlay and auto-trust.

Probe hooks, stdin captures, the probe log and the app-log excerpt are in `logs/desktop-probe/`.
`.claude/settings.local.json` was restored from backup after each hook test (hash `d7ba5573…`, same as
before the spike).

## 8. Follow-up

Run 2026-10-11 to 2026-10-12, same machine, CLI 1.0.0-beta.13, desktop app 2.31226.1 with bundled
Claude Code 2.1.295. The whole-checkout slot from §7 was renamed `wtclone` → `ta` with
`git worktree move`. Evidence is in `logs/test-a/`, `logs/b-targeting.txt`, `logs/b5-close.txt`,
`logs/mem-gui.log`, `logs/c*-*.json`, `logs/d1-cache-key.txt`, `logs/d3-*.diff` and
`logs/d4-settings-diff.txt`.

Two departures from the plan:
- **`wt-remove.sh`** captures git's stderr as well (`2>&1`), so a permission error would show up.
- **User settings** were restored right after B.4, not at cleanup. A user-level `WorktreeCreate`
  hook would hand `ta` to worktree sessions in any repo.

### Results

| Step | Measured | Assumption tested | Verdict |
|---|---|---|---|
| A.1 | `~/.claude/settings.json` existed (no hooks) and was backed up (`43b3f8f2…`); project `settings.local.json` had no hooks (`d7ba5573…`) | — | done |
| A.2–3 | hooks in `~/.upool-probe/`, registered at **user scope**, exec form | — | done |
| A.4 | `worktree list --porcelain`: `ta` detached, unlocked; a 1 s poller logged `ta`'s registration, disk and lock state throughout Test A and B.4 | — | the state **never changed**: no `locked` at any point |
| A.5 session 1 | hook handed out `ta`; reply: `pwd` = `…/ta`, `UPOOL_PROBE=spike-ok`, `UNITY_PROJECT_PATH=…/ta`, `AGENTS.md`, `CLAUDE.md`, `.claude/{settings.json,settings.local.json,skills}` present, `git status` clean. App start timing `worktree=56ms` (4,634 ms for a git checkout in §7) | the app accepts a pre-made slot | **confirmed** |
| A.5 overlay/trust (user scope) | "Skipped per-machine config overlay … — hook returned a path outside the managed worktree directory"; "Skipped auto-trust … — hook-chosen path is outside the managed worktree directory". The managed directory is `<repo>/.claude/worktrees` (Settings → Claude Code → worktree location) | a user-level hook counts as vouched | **confirmed for provenance**: the "repo-committed settings tier" reason from §7 is gone. Overlay and auto-trust are still skipped, now because of the **location** |
| A.5 slot's own project hooks | `ta/.claude/settings.local.json` carries the probe hooks from clone time (D.4); `probe.log` was **never written** across 5 sessions in `ta` | slot-level hooks would run twice | **did not happen**. The reason is unconfirmed: possibly an untrusted slot doesn't run project hooks |
| A.7 before archiving session 1 | 4 attempts: create hook called each time; app refused: `hook printed ".../ta", which is already in use by worktree "env-project-status-6b702e"`, then `worktree_hook_failed`, session aborted. **No `WorktreeRemove`** for refused attempts | — | new: the app enforces one live session per path itself. Refused attempts never call remove, so a lease recorded at create time leaks |
| A.6 archive session 1 | `WorktreeRemove` fired 1 s after archive; log: "Removing worktree … (leased by …)" then "**Removed hook-based worktree**" although the directory remained. `ta` still registered, on disk, unlocked; `git status` from the hook worked (empty) | release hook may keep the slot | **confirmed**: the app trusts exit 0 and doesn't check the directory |
| A.7 session 2 | hook called again; app accepted the same path (`worktree=52ms`); reply `…/ta` | slot can be handed out again | **confirmed** |
| A.8 session 3 (untracked file) | file created; archive gave **no warning**; `WorktreeRemove` fired; `remove-status` = `?? spike-reuse.txt`; file deleted afterwards | app warns about uncommitted work | **contradicted**: no warning. The release hook alone must protect dirty slots |
| A.9 CLI repeat | `claude -p 'reply ok'` → `Not logged in` | — | **skipped** (CLI not logged in) |
| A, `~/Documents` | nothing in `main.log` objected to a slot under `~/Documents` with the hook outside it; the remove hook's `git -C …/ta status` succeeded from the disclaimed process | — | no objection |
| A, session ids | create/remove `session_id` = the app's session id (`e8c6f3e6…` = `local_e8c6f3e6…`); `SessionStart` `session_id` differs (`35f0cfea…`) | — | confirms §7: correlate by path |
| B.1 | headless Editors on clone1/clone2/ta all `ready` within 57 s (ports 7801/7800/7802) | — | done |
| B.2 cwd, variable unset, 3 Editors up | cwd = clone2 → **clone2**; cwd = `ta/Assets/Down` → **ta**; cwd = `/tmp` → `AMBIGUOUS_EDITOR` (lists all 3); cwd = clone3 (no Editor) → `AMBIGUOUS_EDITOR` | cwd selects the slot | **confirmed with ≥2 Editors**. With exactly one Editor (§2, T6.4), cwd is ignored and the call misroutes |
| B.2 (added) cwd vs variable | cwd = clone2, `UNITY_PROJECT_PATH=ta` → **ta** | — | the variable beats cwd |
| B.3 variable for `command eval` | from `/tmp` → clone2 | — | confirmed |
| B.3 `unity run --command editor_status` with the variable | `Not a Unity project (… /private/tmp)`, exit 6, 0 s | `run` honours the variable | **contradicted** |
| B.3 (added) `unity run <clone2> --command editor_status` with clone2's Editor up | success in 0 s, result from the **running** Editor | — | `run --command` routes to a live Editor rather than booting one |
| B.3 `unity build` with the variable (`--list-profiles`) | `Not a Unity project (… /private/tmp)` | `build` honours the variable | **contradicted** |
| B.3 help | `run [options] [project]`, `build [options] [command] [project]`: positional only, no `--project-path` (`logs/help-run.txt`, `logs/help-build.txt`) | — | recorded |
| B.4 desktop session in `ta`, 3 Editors up | `eval` → `ta/Assets`, target `ta`; `unity status` inside the session listed **only `ta`** | the exported variable routes the session's calls | **confirmed** |
| B.5 | `unity close` × 3: each closed the expected PID, gracefully. Idle headless RSS after **19.5 h**: 2,294 / 1,938 / 1,934 MB, 3 shader compilers | — | confirmed; idle Editors grow ~0.5–0.9 GB over a day |
| C.1 | `unity open <path>` (launched via the Hub, `-useHub -hubIPC`, **no `-automated`**): ready in 21 s / 11 s; after ~3 min idle, RSS **1,918 / 1,802 MB**, 1 shader compiler each, ~6.0 GB total with helpers; Hub registry unchanged | idle GUI Editors cost more than batch | **confirmed** (1.8–1.9 GB vs 1.4 GB for batch) |
| C.2 | save-related commands: `save_all`, `save_scene`, `save_prefab_contents`, `create_scene`, `create_prefab` | — | `save_all` is the one |
| C.3 | first eval on the fresh GUI Editor **timed out at 5000 ms but ran** (marker created); later evals 0.17–0.31 s. The slot opened an **Untitled** scene, because batch runs had written `sceneSetups: []` into `Library/LastSceneManagerSetup.txt`; opened `Startup.unity` explicitly and dirtied it | — | new: verify side effects after a first-call timeout |
| C.4 | `unity close` (defaults) on a dirty scene: graceful, **12.1 s, no dialog**, `Startup.unity` unchanged (work discarded) | close may block on a dialog | **contradicted**: no dialog even without `-automated`; it silently discards (help: "exits without saving") |
| C.5 | `unity command save_all` → `saved: true, scenes: [Startup.unity]` in 0.73 s; `close` 0.99 s graceful; `git status`: `M …/Startup.unity` (+32 lines); reverted | save-then-close keeps work | **confirmed** |
| C.6 | with clone1 and clone2 GUI Editors up, closing clone1 left clone2 running; closing clone2 hit PID 11320 | close targets the right Editor | **confirmed** |
| D.1 | `--project-path` → `INVALID_COMMAND_ARGS` (positional only). Key identical twice and equal to main's; **unchanged after a `.cs` edit** and after revert. Components: `editor`, `packages` (`packages-lock.json`), `target` | key detects a stale Library | **contradicted**: detects editor and package changes only |
| D.2 | no asset-refresh command (`--query refresh` → `package_resolve`; `import_asset` is per asset). `AssetDatabase.Refresh()` via eval: 1.73 s then 0.30 s warm; `unity recompile`: 0.25 s, `up_to_date` | — | use eval or `recompile` |
| D.3 | `ta`, clone2, clone3 clean. **clone1: `M ProjectSettings/ProjectSettings.asset`**, written at its close after the D.2 refresh: `scriptingDefineSymbols` reordered (`SENTIS_ANALYTICS_ENABLED` moved to the end) | slots stay clean after Unity runs | **contradicted** for clone1: tracked-file churn happens. Reverted |
| D.4 | `ta/.claude/settings.local.json` ≠ the repo's: it carries the probe hooks that were installed when `ta` was cloned (35-line diff); `settings.json` identical | — | a slot keeps a **stale snapshot** of gitignored config; here those hooks did not run (A.5) |

### Desktop `WorktreeCreate` stdin (Test A, session 1)

```json
{"session_id":"e8c6f3e6-2950-4120-9d6b-d032ae7c6395","transcript_path":"","cwd":"/Users/wes/Documents/Personal/down","hook_event_name":"WorktreeCreate","name":"env-project-status-6b702e"}
```

### Desktop `WorktreeRemove` stdin (session 1 archive, then session 3 with the untracked file)

```json
{"session_id":"e8c6f3e6-2950-4120-9d6b-d032ae7c6395","transcript_path":"","cwd":"/Users/wes/Documents/Personal/down","hook_event_name":"WorktreeRemove","worktree_path":"/Users/wes/Documents/Personal/.upool-spike/down/ta"}
{"session_id":"619a111e-2760-4449-9f92-e7c70913d65d","transcript_path":"","cwd":"/Users/wes/Documents/Personal/down","hook_event_name":"WorktreeRemove","worktree_path":"/Users/wes/Documents/Personal/.upool-spike/down/ta"}
```

`remove-status` for the second: `?? spike-reuse.txt`.

### `main.log` (Test A)

```
23:02:45 [info] Creating worktree for session local_e8c6f3e6-2950-4120-9d6b-d032ae7c6395 from /Users/wes/Documents/Personal/down
23:02:45 [info] [WorktreeHooks] WorktreeCreate hook created worktree at /Users/wes/Documents/Personal/.upool-spike/down/ta
23:02:45 [info] Skipped per-machine config overlay for hook-based worktree "env-project-status-6b702e" — hook returned a path outside the managed worktree directory
23:02:45 [info] Using worktree "env-project-status-6b702e" at /Users/wes/Documents/Personal/.upool-spike/down/ta for session local_e8c6f3e6-…
23:02:45 [info] Skipped auto-trust for hook-based worktree at /Users/wes/Documents/Personal/.upool-spike/down/ta — hook-chosen path is outside the managed worktree directory
23:03:31 [error] WorktreeCreate hook failed: hook printed "/Users/wes/Documents/Personal/.upool-spike/down/ta", which is already in use by worktree "env-project-status-6b702e"
23:03:31 [error] Worktree creation failed with error: worktree_hook_failed, aborting session { baseRepo: '/Users/wes/Documents/Personal/down', sessionId: 'local_4af4bfd1-…' }
   (same refusal at 23:03:36, 23:04:01, 23:04:21)
23:12:33 [info] LocalSessions.archive: sessionId=local_e8c6f3e6-…
23:12:33 [info] Cleaning up worktree env-project-status-6b702e for session local_e8c6f3e6-…
23:12:33 [info] Removing worktree "env-project-status-6b702e" (leased by local_e8c6f3e6-…): /Users/wes/Documents/Personal/.upool-spike/down/ta
23:12:34 [info] Removed hook-based worktree "env-project-status-6b702e"
23:12:43 [info] [WorktreeHooks] WorktreeCreate hook created worktree at /Users/wes/Documents/Personal/.upool-spike/down/ta
23:12:43 [info] Using worktree "pwd-64bb41" at /Users/wes/Documents/Personal/.upool-spike/down/ta for session local_8a6f35b3-…
23:13:33 [info] Removing worktree "spike-reuse-test-file-9a4d6d" (leased by local_619a111e-…): /Users/wes/Documents/Personal/.upool-spike/down/ta
23:13:33 [info] Removed hook-based worktree "spike-reuse-test-file-9a4d6d"
```

Full excerpt: `logs/test-a/main-log-excerpt.txt`.

### What this changes (updated)

| Topic | Call | Why |
|---|---|---|
| Slot creation recipe | **Whole-checkout clone** (§7 TA), plus a **refresh on every handout** | 26 s, 47 MB, no recompile, agent files included, handout in ~50 ms. But a slot freezes a snapshot of gitignored config (D.4) and collects tracked churn (D.3) and empty scene setups (C.3). On release or handout, re-copy `AGENTS.md`, `CLAUDE.md`, `.claude/`, `.agents/` from the repo, and report or revert tracked changes Unity made. |
| Hook registration scope | **User scope**, with a pass-through for other repos (untested) | User scope clears the provenance check that project scope fails. But a user-level `WorktreeCreate` replaces worktree creation for **every** repo. The hook must recognise pooled repos by stdin `cwd`, and for any other repo create a normal worktree itself under `<repo>/.claude/worktrees/<name>`. Whether a failing hook falls back to the app's own creation is untested; assume it aborts the session. |
| Slot location vs `~/Documents` | **Undecided**: keep slots where they are or move them into `<repo>/.claude/worktrees/slot-N` | A slot under `~/Documents` works as long as the hook neither lives nor reads there. Only a slot inside the managed directory (`<repo>/.claude/worktrees`) would get the app's config overlay and auto-trust; that's one more cheap test. That directory is gitignored here (`.claude/`) and outside `Assets/`, so Unity won't import it. |
| Reuse through `WorktreeRemove` exit 0 | **Keep** | The app trusts exit 0, logs "Removed", and hands the same path out again once the old session is archived. It refuses a path held by a live session, so the hook must pick a free slot. Two gaps the tool fills itself: (1) refused handouts never get a remove, so confirm a lease on `SessionStart` (stdin `cwd`) and expire unconfirmed ones; (2) archive gives no uncommitted-work warning, so the release hook checks `git status` and quarantines a dirty slot rather than handing it out. |
| Targeting rules per Unity command | **Change**: per-command rules | `command`, `status`, `recompile`: honour `UNITY_PROJECT_PATH` (beats cwd; `status` is filtered by it). Without it, cwd works only with ≥2 Editors up and misroutes with exactly 1. `test`, `run`, `build`: ignore the variable; pass the project positionally (`run <path> --command` reaches a live Editor). `close`: positional, required. `cache key`: positional. Export the variable **and** have wrappers pass it positionally. |
| Save-then-close | **Change**: release = `unity command save_all`, then `unity close` | `close` alone discards unsaved work silently (12 s, no dialog, even without `-automated`). `save_all` then `close` took 1.7 s and kept the change. After the first eval on a fresh GUI Editor, check side effects: it timed out at 5 s but ran. |
| `max_editors` | **Change** to a RAM budget: about 2.5 GB per Editor plus ~2 GB for helpers; 3 at 64 GB is comfortable, 4 is plausible | Idle GUI Editors: 1.8–1.9 GB after 3 min. Idle headless after 19.5 h: 1.9–2.3 GB. A cold import peaks at 4.2–4.5 GB. Three Editors ≈ 6–7 GB with helpers. A play session or a long interactive day will add to this, so measure under real use before going past 3. |
| Staleness detection | **Change**: don't rely on `unity cache key` | It hashes the editor, `packages-lock.json` and build target only. Record the seed source's `git rev-parse HEAD` plus a dirty check, and compare `Assets/**/*.cs` mtimes against `Library/ScriptAssemblies` (that's how §1 found the stale main). Use the cache key only to catch editor or package changes, which force a big reimport. |

## 9. Follow-up 2

Run 2026-10-12, same setup. Evidence is in `logs/test-e/` (hooks, stdin, slot/registry poll, registry
snapshots, `main.log` excerpt), `logs/test-f/`, `logs/g2-*.diff`, `logs/g-repro-headless.log`.

Three departures from the plan:
- **Test G** was run first, so its GUI windows wouldn't pop up during your desktop steps.
- **Extra repro in G:** the D.2 sequence was repeated to look for the churn trigger.
- **Extra markers in E:** before the second session, distinct `SessionStart` markers went into the
  slot's `settings.json`, the slot's `settings.local.json` and the repo's `settings.local.json`
  (backed up and restored at once), to find where a slot session's project hooks come from.

User settings were restored right after Test F. Test H was skipped: no other repo is set up in the
desktop app.

### Results

| Step | Measured | Assumption tested | Verdict |
|---|---|---|---|
| E.1 | `slot-1` at `$ROOT/.claude/worktrees/slot-1`: clone 17.3 s, index 7.3 s, disk ≈0, `git status` clean, `.claude/` copied without `worktrees/`. `.claude/worktrees` is gitignored by `.gitignore:43 .claude/`. Main checkout still clean | — | done |
| E.2–3 | marker `SessionStart` in the slot's `settings.local.json`; user-scope create prints `slot-1`; remove is an exit-0 no-op that logs `git status` | — | done |
| E.4 registry | `~/Library/Application Support/Claude/git-worktrees.json` (`schemaVersion: 2`, keys `worktrees`, `untrackedDirGc{sightings,roots,cwds}`, `originUrls`). Before: `worktrees: {}`; `untrackedDirGc.cwds` = repo and `ta` | the three `ta` leases accumulated | **no**: lease entries are deleted on archive |
| E.5 session 1 | `pwd` = `slot-1`; `.claude` = `settings.json`, `settings.local.json`, `skills`; `slot-hook.log` absent; app `worktree=112ms` | slot's own project hook runs | **contradicted** |
| E.5 overlay / trust | `[worktreeOverlay] 21 gitignored config entries do not resolve to a regular file inside the checkout (a link leaving it or dangling, a FIFO, a directory); not copied`; `Skipped 3 gitignored .claude files already present in worktree`; `Saved workspace trust …`; `Auto-trusted worktree … (inherited from /Users/wes/Documents/Personal/down)` | managed dir gets overlay and auto-trust | **confirmed**. The overlay doesn't overwrite files already present, and skips the 21 symlinks under `.claude/skills` and `.agents` |
| E.5 registry during lease | entry `{name, path, leasedBy: local_<id>, baseRepo, branch: <name>, sourceBranch: main, createdAt, hookBased: true, fromCommittableTier: false, anchors[…trustedAt], autoTrusted: true}`; **no git branch** created | — | recorded |
| E (added) hook source | second session: only the **repo's** `settings.local.json` marker ran, with `cwd=/Users/wes/Documents/Personal/down`; the slot's `settings.json` and `settings.local.json` markers did not | — | **new**: a slot session takes project hooks from the main checkout, run in the main checkout's cwd |
| E (added) memory | slot sessions (managed `slot-1` and sibling `ta`) load the slot's `CLAUDE.md`/`AGENTS.md` and the **repo's** auto-memory (`~/.claude/projects/-Users-wes-Documents-Personal-down/memory/MEMORY.md`); transcripts are keyed by slot path | — | **corrects §7**: memory follows the repo |
| E.6 after archive | `WorktreeRemove` fired; log `Removing worktree … (leased by …)` then `Removed hook-based worktree`; `slot-1` stayed registered and on disk, never locked (1 s poll); registry entry deleted (`worktrees: {}`) | app removes a managed-dir slot itself | **no**: it only calls the hook, as for sibling slots |
| E.7 | first try refused (`…slot-1", which is already in use by worktree "system-diagnostics-9f8a09"`) because session 1 was still live; after archive: accepted (`worktree=75ms`), archived, kept | reuse works in the managed dir | **confirmed** |
| F.2–3 creates | **2** `WorktreeCreate` calls (one per subagent, same second), run by the **bundled CLI process** (`…/claude.app/Contents/MacOS/claude`), not the app's `disclaimer` | one create per subagent | **confirmed** |
| F.3 stdin | `session_id` (parent CLI session), `transcript_path` (parent, non-empty), `cwd` (repo), `scratchpad_dir`, `prompt_id` (same for both), `hook_event_name`, `name: agent-<17 hex>`; **no `agent_id`/`agent_type`** | stdin marks a subagent | **partly**: no explicit field. Tell-tales are `name` = `agent-…`, plus `prompt_id` (absent from session creates) |
| F.3 removes | **0** `WorktreeRemove`: none when the subagents finished, none when the session was archived at 22:16:22. Both `sub-agent-*` worktrees are still on disk and registered, with zero changes; the task notification carried `<worktree><worktreePath>…</worktreePath></worktree>` | remove fires when a subagent finishes | **contradicted**: hook-made subagent worktrees leak |
| G.1 batch | `clone1` reverted; batch run 15 s → **clean**; `LastSceneManagerSetup.txt` kept `Startup.unity` | Unity churns tracked files every open | **contradicted** |
| G.2 GUI | open → ready → 30 s → close (45 s) → **clean**; same scene list kept (second GUI run not needed) | — | **contradicted** |
| G (added) D.2 repro | headless Editor (no `-quit`), 2 × `AssetDatabase.Refresh()`, `recompile`, graceful close → **clean** | — | the define reorder didn't reproduce; seen once, after a 19.5 h headless session |
| G.3 scene list | GUI session and batch `-quit` keep the scene list; a **headless Editor closed gracefully writes `sceneSetups: []`** (repro) | — | new: the empty list in §8 C.3 came from Test B's headless Editors |
| G.4 | nothing to revert | — | — |
| H | — | pass-through for other repos | **couldn't test**: no other repo set up |

### Subagent `WorktreeCreate` stdin (verbatim)

```json
{"session_id":"f6d1f92c-71cf-4b2a-aa5f-b2dc9cf79038","transcript_path":"/Users/wes/.claude/projects/-Users-wes-Documents-Personal-down/f6d1f92c-71cf-4b2a-aa5f-b2dc9cf79038.jsonl","cwd":"/Users/wes/Documents/Personal/down","scratchpad_dir":"/private/tmp/claude-501/-Users-wes-Documents-Personal-down/f6d1f92c-71cf-4b2a-aa5f-b2dc9cf79038/scratchpad","prompt_id":"3b9b23da-bede-4b6e-9d0f-3dde9a38a776","hook_event_name":"WorktreeCreate","name":"agent-a59f8e76142da8348"}
{"session_id":"f6d1f92c-71cf-4b2a-aa5f-b2dc9cf79038","transcript_path":"/Users/wes/.claude/projects/-Users-wes-Documents-Personal-down/f6d1f92c-71cf-4b2a-aa5f-b2dc9cf79038.jsonl","cwd":"/Users/wes/Documents/Personal/down","scratchpad_dir":"/private/tmp/claude-501/-Users-wes-Documents-Personal-down/f6d1f92c-71cf-4b2a-aa5f-b2dc9cf79038/scratchpad","prompt_id":"3b9b23da-bede-4b6e-9d0f-3dde9a38a776","hook_event_name":"WorktreeCreate","name":"agent-a4fc3fea4c347d21c"}
```

How to tell the requests apart:

| Request | `transcript_path` | `prompt_id` | `scratchpad_dir` | `name` | Run by |
|---|---|---|---|---|---|
| Desktop session | `""` | no | no | prompt-derived (`pwd-64bb41`) | app `disclaimer` (no Documents access) |
| CLI `--worktree` | set | no | no | user-given | CLI |
| Subagent | set | **yes** | yes | `agent-<hex>` | CLI |

### Registry entries

Before Test E (and after every archive), entries mentioning the repo:

```json
{"worktrees":{},"untrackedDirGc":{"sightings":{},"roots":{},"cwds":{"/Users/wes/Documents/Personal/down":1791852689694,"/Users/wes/Documents/Personal/.upool-spike/down/ta":1791852689694}},"originUrls":{"/users/wes/documents/personal/down":{"commonDir":"/users/wes/documents/personal/down/.git","url":"<redacted>@github.com:weslo/down.git"}}}
```

During a lease (session 1):

```json
{"system-diagnostics-9f8a09":{"name":"system-diagnostics-9f8a09","path":"/Users/wes/Documents/Personal/down/.claude/worktrees/slot-1","leasedBy":"local_49b55ea5-fc66-40a8-8f6c-b4afc31ec456","baseRepo":"/Users/wes/Documents/Personal/down","branch":"system-diagnostics-9f8a09","sourceBranch":"main","createdAt":1791854803710,"hookBased":true,"fromCommittableTier":false,"anchors":[{"gitRoot":"/Users/wes/Documents/Personal/down","commonDir":"/Users/wes/Documents/Personal/down/.git","trustedAt":1791854803672},{"gitRoot":"/Users/wes/Documents/Personal/down/.claude/worktrees/slot-1","commonDir":"/Users/wes/Documents/Personal/down/.git","hookCreated":true,"trustedAt":1791854803710}],"autoTrusted":true}}
```

After all tests: `worktrees: {}`. Subagent worktrees never appear in it.

### `main.log` (Test E, session 1 and archive)

```
21:26:43 Creating worktree for session local_49b55ea5-… from /Users/wes/Documents/Personal/down
21:26:43 [WorktreeHooks] WorktreeCreate hook created worktree at /Users/wes/Documents/Personal/down/.claude/worktrees/slot-1
21:26:43 [worktreeOverlay] 21 gitignored config entries do not resolve to a regular file inside the checkout (a link leaving it or dangling, a FIFO, a directory); not copied
21:26:43 Skipped 3 gitignored .claude files already present in worktree
21:26:43 Created hook-based worktree "system-diagnostics-9f8a09" at …/.claude/worktrees/slot-1
21:26:43 [GitWorktreeManager] recorded origin's URL for this repository: <redacted>@github.com:weslo/down.git { baseRepo: '…/.claude/worktrees/slot-1' }
21:26:43 Saved workspace trust for …/.claude/worktrees/slot-1
21:26:43 Auto-trusted worktree with key "…/.claude/worktrees/slot-1" (inherited from /Users/wes/Documents/Personal/down)
21:26:43 Starting local session local_49b55ea5-… in …/.claude/worktrees/slot-1
21:27:04 [error] WorktreeCreate hook failed: hook printed "…/.claude/worktrees/slot-1", which is already in use by worktree "system-diagnostics-9f8a09"
21:27:29 Cleaning up worktree system-diagnostics-9f8a09 for session local_49b55ea5-…
21:27:29 Removing worktree "system-diagnostics-9f8a09" (leased by local_49b55ea5-…): …/.claude/worktrees/slot-1
21:27:29 Removed hook-based worktree "system-diagnostics-9f8a09"
```

Test F (`main.log`) has no worktree lines at all: subagent worktrees are handled entirely inside the CLI.

### What this changes (updated)

| Topic | Call | Why |
|---|---|---|
| Slot location | **Change** to the managed directory, `<repo>/.claude/worktrees/slot-N` | It gets auto-trust (inherited from the repo) and the config overlay, keeps the ~75 ms handout and exit-0 reuse, and the app never deletes it. Requirements: the tool must check that `.claude/worktrees` is gitignored in each pooled repo, and the hook script must still live outside `~/Documents`. Watch for IDEs that index gitignored folders. |
| Project hooks and config overlay in slots | **Plan for the repo's settings, not the slot's** | A slot session runs project hooks from the **main checkout's** `.claude/settings*.json`, in the main checkout's cwd. A slot's own copies never run, so nothing runs twice and hooks can't be varied per slot. The overlay only fills in files the slot lacks: it doesn't refresh stale copies and skips symlinks that leave the checkout. Memory follows the repo; `CLAUDE.md`/`AGENTS.md` come from the slot, so the tool must keep them current itself. |
| Subagent requests | **Change**: don't hand out pool slots to subagents | Detect them by `prompt_id` (or `name` = `agent-…`). They run in the CLI process (full file access) and never get a `WorktreeRemove`, even after the session is archived. A slot given to a subagent would never be released. Make an ordinary lightweight worktree under `<repo>/.claude/worktrees/agent-*` (no Unity Library), and garbage-collect it when it's clean and its parent `session_id` has ended, or past an age limit. If a subagent needs Unity, give it a slot through an explicit tool call that it releases itself. |
| Release dirty check for `ProjectSettings.asset` | **Keep the check**, plus one noise rule | Unity didn't churn on batch, GUI, or headless-plus-refresh runs; the reorder happened once, after a 19.5 h headless session. Treat a `ProjectSettings.asset` diff whose only change is the **order** of `scriptingDefineSymbols` (same set) as noise and revert it. Any other tracked change means dirty: quarantine the slot. Also restore `Library/LastSceneManagerSetup.txt` after a headless close, or open the scene explicitly, since a headless close empties it. |
| User-scope hook blast radius | **New risk** | The user-scope create hook also caught this repo's **subagents** (Test F) and would catch every other repo's sessions. The F session itself noticed and warned that the hook was still installed. The pass-through branch (Test H) remains untested. |

## 10. GC and symlink checks

Run 2026-10-12. Evidence: `logs/test-gc/` (worktree lists, registry copies, `main.log` offsets for
each step) and `logs/symlink-check.tsv`.

### GC check

| Step | Measured |
|---|---|
| Before relaunch (22:21) | `untrackedDirGc`: `sightings {}`, `roots {}`, `cwds` = repo, `ta`, **`slot-1`** (added after Test E's sessions). 9 worktrees registered, `slot-1` among them |
| Relaunch 1 (app started 22:29:19, read 22:3x) | registry rewritten at 22:30: `cwds` timestamps refreshed, `sightings`/`roots` still empty, `worktrees {}`. `slot-1` on disk and registered; worktree list identical. `main.log`: no `untracked`, `sweep`, `prune` or `slot-1` lines (the only "gc" hits are V8 heap stats) |
| Relaunch 2 (app started 22:41:03) | registry **not rewritten** (mtime still 22:30), contents identical. `slot-1` on disk and registered; worktree list identical. No relevant `main.log` lines |

The GC never sighted `slot-1` across two relaunches. Its trigger is unknown: `cwds` refreshed on the
first relaunch only, so it may run on a timer. Judging by its name (`untrackedDirGc`), it probably
targets directories that git doesn't list as worktrees, and `slot-1` is a registered worktree. That is
an inference, not observed.

**Call: the managed directory stays safe to use**, provided slots remain registered git worktrees.
The tool should check on startup that every slot is still on disk and in `git worktree list`, and
recreate any that vanished.

### Symlink check

| Tree | Links under `.claude`/`.agents` | Kind | Resolve |
|---|---|---|---|
| main | 21 | relative, inside the checkout (`.claude/skills/<x>` → `../../.agents/skills/<x>`) | 21/21 |
| `slot-1` | 21 | same; each resolves to the slot's **own** `.agents/skills/<x>` | 21/21 |
| `ta` | 21 | same | 21/21 |

None dangle, none are absolute, and none leave the checkout. All 21 point at directories. That is why
the app's overlay logged "21 gitignored config entries do not resolve to a regular file … (… a
directory); not copied": it skips links to directories, and they weren't broken. `cp -cR` preserves
the links, and because they're relative they follow the clone.

**Call: no symlink fix-up needed for this repo.** The tool should copy `.agents/` along with `.claude/`,
and only warn about absolute or checkout-escaping links (zero here).
