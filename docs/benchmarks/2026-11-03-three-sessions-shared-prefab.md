# 2026-11-03: three sessions, shared prefab

## Run

| Field | Value |
|---|---|
| Date | 2026-11-03, 17:22Z to 2026-11-04, 03:32Z |
| upool | 0.6.1 (Homebrew; 0.6.0 until the first stop, see Observations) |
| Unity Editor | 6000.4.11f1 |
| Unity CLI | 1.0.0-beta.13 |
| Claude Code | 2.1.309 in the desktop app until it updated itself to 2.1.317 at 11:26; the `claude` CLI that `doctor` checks is 2.1.305; sessions on Opus 5.5 at extra effort, `auto` permissions |

## Machine class

| Field | Value |
|---|---|
| Chip | Apple M1 Max |
| Memory | 64 GB |
| Disk | SSD, APFS |

## Project size class

| Field | Value |
|---|---|
| Library size | 9.2 GB |
| Library files | 72,826 |
| Assets | 6,032 |

## Workload shape

Three sessions, each on one issue of a networked first-person game, each started by the desktop
app with a worktree so that upool's create hook handed it a slot, each briefed to open its Editor
with `upool open`, to host on its own port and to run a development build of the project as its
second peer. Session A started first; B and C started together five minutes later, once a second
and third slot had been warmed. An orchestrating session in the main checkout measured from
outside.

Shared files: A and B both edited the same two actor prefabs and the same C# hit-handling file;
A and B both edited a second C# file (a debug overlay) and one agent-guidance note; B and C both
edited a C# character base class. Every prefab edit went through the session's own Editor.

Changes landed in start order: A first, then B rebased onto it, then C merged main in. A review
of B's change added two follow-up rounds in the same session before it landed.

## Measurements

### Time to ready, per slot

| Slot | Session start to Editor ready | `open`'s `ready` step, when `upool open` opened it |
|---|---|---|
| A | about 84 s (lease 24 s after the start call; headless open 8.6 s; the rest the session's own reading) | 8,625 ms (headless) |
| B | about 5.5 min, all of it the session's reading and planning before it opened the Editor | 21,189 ms (gui) |
| C | about 8 min, likewise | 20,787 ms (gui) |

The hook's take logs nothing; the lease on A's slot appeared 24 s after the `start_session`
call, which is the app's session start plus the take in one number. B's and C's leases appeared
41 s after their shared start call, with no clone to wait for.

### Probe latency

`UNITY_PROJECT_PATH=<slot A> unity command eval 'return Application.unityVersion;'`, against the
same headless Editor both times.

| Editors up | Call 1 | Call 2 | Call 3 |
|---|---|---|---|
| 1 | 1.181 s (the first eval compiles) | 0.066 s | 0.064 s |
| all (3) | 0.129 s | 0.078 s | 0.065 s |

No slowdown from three Editors, and no 5-second throttling of the hidden headless Editor.

### Memory per Editor

Budget: `editor_ram_budget_gb` 16 (the default at 64 GB), `max_editors` 3, `editor_ram_gb` 2.5,
helper allowance 2. Refused: none. Queued: none; every `admit` took 6 to 12 ms.

Each cell is the Editor's resident size with its children's, one level down, summed, in GB.
"All ready" is 19:45Z, "midway" 20:10Z, "before the end" 20:38Z, as the first pull request
opened.

| Editor | All ready | Midway | Before the end |
|---|---|---|---|
| A (headless at the first sample, gui after) | 1.96 | 5.09 | 6.50 |
| B (gui) | 2.83 | 5.46 | 6.40 |
| C (gui) | 2.68 | 5.93 | 6.13 |
| total | 7.5 | 16.5 | 19.0 |

The budget charged 7.5 GB plus the 2 GB helper allowance against 16; the three working GUI
Editors with their children cost 19.0 GB by the end, 2.5 times the model. A fresh GUI Editor with
its children already exceeds `editor_ram_gb` by 0.2 to 0.3 GB. The 30-second sampler's peak over
the whole run was 18.2 GB. Nothing was short on a 64 GB machine.

### The shared files

- **The two prefabs (A and B).** A replaced one collider document in each through its Editor; B
  added generated piece documents to each through its Editor, driven by a generator whose recipe
  lives in an asset beside the prefab. The lane guard never fired for either: every write was the
  Editor's. When A landed first, B discarded git's automatic text merge of both prefabs, took
  main's copies and re-ran its generator in its Editor; it produced the same pieces (21 and 24)
  on the same bones, and no document A had written changed. The whole rebase turn, with an Editor
  reopen, a generator run, EditMode tests and a play probe, took about ten minutes; git reported
  one text conflict, in the guidance note, which B resolved by keeping both sides.
- **The C# hit handler (A and B).** Git merged it as text: B's one call sat inside a method whose
  signature A had changed. No conflict.
- **The C# overlay (A and B).** Merged as text, both docstring changes kept.
- **The C# character base class (B and C).** Git merged it without a conflict when C merged main
  in: B's change was a comment away from C's lines.
- **Landing B's rebase** needed a force-push of its branch, which the desktop app's `auto`
  permission classifier denied in the session and in the orchestrator alike. The orchestrator
  pushed the rebased tree as a merge commit (parents: the old head and main) so the push was a
  fast-forward, and C was told to merge main rather than rebase for the same reason.

### Second peers

Each session built a development build of the project and joined it to its own Editor as host,
on its own port. The sampler (30-second grain) saw a player process up in 94 of its 1,217 rows,
two sessions' players up at the same time for about a minute (20:26:49Z to 20:27:52Z), and
every peer ran while all three Editors were up. C ran its two-peer check again during its
landing. Nothing refused or delayed a peer; no session's game joined another's.

### upool's step timings

All of the run's lines from the timings log. `refresh` lines have no slot; the hooks' take and
release log nothing. Session work ran 19:32Z to 20:38Z; the lines after that are the landings
and the review follow-ups.

| Time (Z) | Operation | Slot | Steps (name: ms) | Total ms |
|---|---|---|---|---|
| 17:23:14 | refresh --fetch | - | mirror 967, move 200 (re-warm failed, unlogged) | 35,497 |
| 17:42:54 | refresh (fix build) | - | close 2358, mirror 1023 | 3,636 |
| 18:59:03 | refresh (top-up) | - | mirror 0, clone 72184 (seed: main checkout) | 72,239 |
| 19:32:45 | refresh (hook top-up) | - | mirror 1444 | 1,476 |
| 19:33:14 | refresh, `UPOOL_READY=2` | - | mirror 477, clone 26481 (seed: a warm slot) | 27,002 |
| 19:33:36 | open, headless | A | admit 9, ready 8625 | 8,644 |
| 19:37:41 | refresh (hook top-up) | - | mirror 0 | 29 |
| 19:37:41 | refresh (hook top-up) | - | mirror 0 | 23 |
| 19:42:09 | open, gui | B | admit 6, ready 21189 | 21,196 |
| 19:44:53 | open, gui | C | admit 12, ready 20787 | 20,801 |
| 19:58:22 | close | A | save_all 225, close 1280 | 1,506 |
| 19:58:28 | open, gui | A | admit 10, ready 11619 | 11,630 |
| 20:38:52 | close | A | save_all 136, close 2091 | 2,228 |
| 23:19:13 | close | B | save_all 1453, close 9758 | 11,213 |
| 23:19:17 | close | C | save_all 860, close 7409 | 8,270 |
| 23:52:02 | open, gui | B | admit 6, ready 10590 | 10,598 |
| 23:57:55 | close | B | save_all 146, close 1398 | 1,545 |
| 00:27:36 | open, gui | B | admit 6, ready 6775 | 6,781 |
| 00:34:38 | close | B | save_all 146, close 1253 | 1,400 |
| 00:36:00 | open, gui | B | admit 9, ready 7838 | 7,849 |
| 00:42:22 | close (failed) | B | none logged (`save_all` timed out) | 30,047 |
| 02:10:45 | open, gui | B | admit 8, ready 12798 | 12,809 |
| 02:12:28 | close | B | save_all 143, close 1189 | 1,333 |
| 02:12:35 | open, gui | B | admit 9, ready 6674 | 6,684 |
| 02:17:52 | close | B | save_all 244, close 6301 | 6,547 |
| 03:20:55 | open, gui | C | admit 10, ready 12157 | 12,168 |
| 03:32:25 | close | C | save_all 239, close 5218 | 5,459 |

A clone from a warm slot as seed (26.5 s) was 2.7 times faster than one from the main checkout
(72.2 s), which also needed a headless seed refresh first. A GUI open on a warm slot took 6.7 to
21.2 s to ready; the longer ones include a compile of changed scripts.

## Observations

**Two stops before the first session, under the run's rule that a blocking operational problem
stops the run for a fix rather than a workaround.**

- `upool refresh --fetch` re-warmed the one ready slot and could not close its headless Editor:
  the close was sent within seconds of ready and got `PROJECTS_CLOSE_UNREACHABLE`; `refresh` did
  not retry, the slot stayed `warming` with the Editor live, the top-up counted it as ready and
  cloned nothing, and the hook's take would have found no slot. Fixed during the run as #273 and
  released as 0.6.1: the re-warm's close retries and forces, and `refresh` recovers such a slot
  (the 17:42 line above, which closed the orphan in 2.4 s). The run continued on the Homebrew
  0.6.1 binary.
- The first `start_session` failed after the app's five-minute start timeout. Root cause: the
  `WorktreeCreate` hook, run by the desktop app's helper, was held silently by a pending macOS
  consent prompt asking whether upool may access the Documents folder. The app cancelled the
  start at 300 s and logged the hook as failed with no output at 600 s; the prompt was accepted
  45 minutes later, and the next session's hook, three minutes after that, answered at once.
  `sample` had shown git blocked in `getcwd`, but that was the shell reproduction: #286 found
  from the TCC log that the app's hook was held in Go's start-up `Getwd`, before any of upool's
  code ran, where no timeout of upool's can reach it. upool could name the likely cause where
  it can, and the install docs could say the first pooled session may raise that prompt, which
  #286 has `doctor` and `init` raise on purpose.

**During the work phase, from the sessions' own reports.**

- `upool open` is headless by default, and neither the hook's skill text nor `--help` says so.
  A opened headless, found no Game view, closed and reopened `--gui`; B and C found `--gui`
  through `--help`.
- Every `upool open` prints `warning: Warning: "<slot>" was not found in the Hub project
  registry, opening as a file path.`, with the prefix doubled.
- `status` shows the lease holder as the app's session id, which differs from the CLI's own
  session id inside the session, so a session cannot tell which lease is its own.
- Plain `status` left out a slot while it was `creating` and the running refresh that `--all`
  showed.
- The slot arrives detached at the base branch with no branch, although the app had named the
  workspace; each session created its branch itself, and the issue skill's `git checkout main`
  cannot run in a slot.
- The lane guard never fired: every asset change went through the Editor, as the skill predicts.
- Where upool did not get in the way: `UNITY_PROJECT_PATH` routed every command, recompile, test
  run and build to the session's own Editor; `unity status` inside a session listed only its own;
  the warm Library meant no import; no Unity call timed out and none needed a retry for upool's
  sake; three two-peer checks ran, two of them at the same time.

**After the work phase.**

- B and C left their GUI Editors running after opening their pull requests, each holding about
  6 GB and a seat of `max_editors` for 2 h 50 min while the sessions only waited on review;
  nothing in the skill text says to close when done, and nothing reclaims an Editor while the
  lease is held. Both closed on one message. A had closed its own.
- `upool close` hung once: its `save_all` opened a native Save panel for a scene with no path,
  left by the post-build restore of the open scene after a development build. The scene was not
  dirty, so the session's dirty check seconds earlier saw nothing. `save_all` timed out at 30 s,
  upool refused to close, every later Unity call timed out, and `close` has no `--force`. The
  session stopped and asked for a hand; the panel was dismissed by hand. The Editor was not in
  automation mode: `open` passes no Editor arguments for gui and only `-batchmode` for headless,
  and has no passthrough for a session to add `-automated`.
- After a merge-in, `upool open --gui` reported ready while the merged assets were still
  importing, and the session's next Unity call was refused as still settling for a few seconds.
- A reopened Editor came up on a different port than before; commands still reached it by the
  slot's path.
- Not upool: the desktop app's `auto` permission classifier denied `git push --force-with-lease`
  and `gh run cancel` in every session, which shaped how the rebase landed and left a CI run
  stalled on its runner for 50 minutes until a person cancelled it.
- **Archiving the done sessions deleted the slots and left their leases behind.** At the end of
  the run all three slots were still leased by sessions that were done but not archived. The
  orchestrator archived them through the app's session tool: one call each, no approval needed
  in `auto` mode, about a second each. For each, the app logged `Cleaning up worktree`,
  `Removing worktree … (leased by …): <slot path>` and `Removed hook-based worktree`, deleted the
  slot's directory from disk (a clone with the 9.2 GB Library), and dropped its own record of
  it. No `WorktreeRemove` reached upool: `state.json` did not change, and the app's log has no
  hook line. upool was left with three `leased` slots whose holders were dead and whose paths
  were gone, and nothing in the CLI recovers that: `doctor` fails the three slot checks and
  `--fix` drops only missing ready slots; `prune` finds nothing; `take` reports
  `could not reclaim slot-N, though its holder, process N, is gone: <path> is not on disk`,
  skips the three as leased, and declines to clone because the pool is at `max_slots`. The
  pool could not hand out a slot until its state was edited by hand. Contract (a) was captured
  on an earlier app build, where the hook ran about a second after the archive and the app
  accepted exit 0 with the directory still present; on this build (Claude Code 2.1.317 in the
  desktop app of early November 2026) an archive from the session tool removed the directory
  itself. The Archive button was not tried, and upool logs nothing for its hooks, so whether the
  hook ran at all is not answerable from the machine.

## What this changes for upool

- **`editor_ram_gb` 2.5 describes a fresh headless Editor, not an hour of play mode, builds and
  shader compilation.** Three working GUI Editors with their children reached 19 GB, about 6 GB
  each, 2.5 times the charge. The call this suggests: charge GUI Editors about 6 GB and headless
  ones about 2.5, or let the charge grow with a running Editor's sampled size, so that the budget
  refuses the Editor that would not fit rather than the one the model says would not. At the
  default 16 GB on a 64 GB machine that admits two GUI Editors, not three; the budget's fraction
  of the machine may need to rise with the charge.
- **The re-warm's close must retry** (done: #273, 0.6.1). The run's first stop was one failed
  close holding the only slot.
- **The hook's git calls need a bound and a message.** A pending consent prompt cost an hour of
  silence; a timeout of seconds naming the folder and the likely cause would have cost a minute.
- **`open` should pass `-automated`** in both modes, and say its default mode and what each is
  for, in `--help` and in the hook's skill text. Opening headless by default cost one session a
  close and a reopen, and an Editor without automation mode is what let a native panel block a
  close.
- **`close` needs `--force`, and `save_all` should skip or refuse a scene with no path** and say
  so, rather than letting the Editor prompt; the timeout after `save_all` should name the panel
  as the likely cause.
- **A leased slot whose directory is gone and whose holder is dead must be reclaimable.** The
  app deleted three hook-based worktrees on archive without a remove reaching upool, and the
  pool was wedged at `max_slots` with nothing ready. `take`, `refresh` and `doctor --fix` should
  drop such a slot, or re-clone it in place, so the top-up can reach `ready` again, and `status`
  should show it as missing rather than leased. The hook subcommands should log their
  invocations, so that "did the hook run" has an answer. Contract (a)'s archive rows need
  re-capturing on a current app build, the Archive button and the session tool each, clean and
  dirty; until then the skill texts should say that archiving a session deletes its slot and the
  pool needs a `refresh` after.
- **The skill text should tell a session to close its Editor when its work is done** and reopen
  for a follow-up; `status` could flag a leased slot whose Editor has been idle long. Two idle
  Editors held 12 GB and two of three seats for almost three hours.
- **`open` should report ready only once an import in flight has settled**, or say that it has
  not.
- **Smaller:** drop the doubled prefix on the Hub warning; show creating and warming slots and
  the running refresh in plain `status`; show the slot a session is in beside the lease, since
  the holder id is not the session's own; hand the slot out on the named branch or say in the
  skill text that the session branches itself.
- **The lane guard saw nothing**, as expected: with every asset edit going through the Editor,
  the shared-prefab story was carried by the project's own generator and a re-run, not by the
  guard. The record of what happened at the shared files is the more useful measurement.
- **No change to the probe or to time-to-ready**: three Editors did not slow a command, and the
  clone and open times are the costs the design expected; a warm-slot seed for clones is worth
  keeping as the default.
