---
name: upool-benchmark
description: How to benchmark upool on a body of work by running its sessions on upool slots and measuring, from outside every slot, how the pool held up. Read it before planning or starting such a run, when a project wants numbers on upool under its own sessions, and before writing the run's record for the upool repository.
---

# Benchmarking upool on a body of work

A benchmark runs a project's own sessions, each in a upool slot with its own Editor, and measures
from outside them how fast slots and Editors came up, what several Editors cost and whether the
RAM budget held, and how sessions that touched the same files got on. Run it from the project's
main checkout, never from a slot. The record goes to the upool repository, where it describes the
project by shape and names nothing, so that numbers from many projects add up.

## Describe the workload by shape

Describe the body of work in counts and kinds, never by identity. The project's own plan holds the
identities; this skill never asks for them, and the record never carries them.

- how many sessions, and in what order they start;
- which sessions edit the same files, and of what kind: code, prefab, scene, other asset;
- which sessions need a second Unity peer, such as a second Editor or a player of the same
  project, and which host a server;
- in what order their changes are meant to land.

"Three sessions; two edit the same prefab, one of them a scene as well; one hosts a server and
needs a second peer; changes land in start order" is the whole description.

## What to measure, from outside the sessions

Measure from your own shell in the main checkout, with `UNITY_PROJECT_PATH` unset. upool's own
step timings come from the pool's timings log, `~/.upool/pools/<repo-key>/timings.jsonl`, and
the `upool: timings:` line each command prints on stderr; the sessions measure nothing.

- **Time from `take` to the Editor reporting ready, per slot.** Note the clock when you start
  each session, and poll `unity status --format json` until an instance whose `project` is the
  session's slot is `ready`; the difference is the slot's time to ready. The log's `open` line for
  the slot, when the session opened its Editor with `upool open`, gives the Editor's own part of
  it as its `ready` step.
- **Latency of a fixed probe, with one Editor open and with all of them.** The probe is the eval
  the spike used, against one slot by its path:

  ```bash
  UNITY_PROJECT_PATH=<slot> unity command eval 'return Application.unityVersion;'
  ```

  Time three calls against the same slot, once when its Editor is ready and alone, and once when
  every Editor is up. Skip a first call that times out: the first call to a fresh Editor may, and
  still run.
- **Memory per Editor, and whether the RAM budget refused or queued any.** Sample each Editor's
  resident size by the `pid` that `unity status --format json` lists, `ps -o rss= -p <pid>`, in
  kilobytes, at three moments: when every Editor is ready, midway, and before the sessions end.
  The budget's decisions are in the log and on the sessions' stderr: an `open` line with `ok`
  false and no `admit` step was refused, and its session saw `refuses another Editor` naming the
  limit; one whose `admit` step took seconds waited behind a cold import, and its session saw
  `waiting to open the Editor of`. Note the budget the run ran under, `editor_ram_budget_gb` and
  `max_editors`, from the user config or the defaults.
- **How a conflict on a shared file went through the confirm step.** For each file two sessions
  edit: what each session did when it reached the file (edited it through its Editor, was refused
  by the lane guard, or took a slot in the scene lane), and, when the later change was confirmed
  to land, whether git or Unity reported a conflict, who resolved it and how long that took.
- **Whether the sessions' second peers ran at the same time.** For each session that needs a
  second peer: whether the peer was up while the session's own Editor and the other sessions'
  were, from the `unity status` samples, or what refused or delayed it.

## How to run it

**Before you start.** upool, the Unity CLI and Claude Code are installed, and their versions
noted with the Editor's; the project is a pool, which `upool status` lists from its main checkout;
its Editor is closed and `unity status` lists no Editor; `jq` is on `PATH`; and the plan names
each session's work. Note the machine class, the project's size class, and the timings log's line
count, so that the run's lines are the ones after it:

```bash
sysctl -n machdep.cpu.brand_string hw.memsize; diskutil info / | grep -E 'Solid State|File System'
du -sh Library; find Library -type f | wc -l; git ls-files Assets | grep -vc '\.meta$'
key=$(jq -r --arg p "$(pwd -P | tr '[:upper:]' '[:lower:]')" \
  '.repos[] | select(.path == $p) | .key' ~/.upool/registry.json)
log=~/.upool/pools/"$key"/timings.jsonl; wc -l < "$log"
```

**In order.**

1. Start the sessions in the plan's order, each with a worktree, the way the project starts its
   sessions, so that upool's create hook hands each a slot; give each its brief, and note the
   clock at each start. Watch `unity status` for each slot's Editor: the first measurement comes
   from this watch.
2. With the first Editor ready and alone, run the probe. Keep the sessions going.
3. With every Editor up, run the probe again, then take the first memory sample.
4. Let the sessions work. Take the second memory sample midway, and note as they happen: what
   each session did at the shared files, each second peer's start and end, every line upool
   printed on stderr, and every time a session retried a Unity call, hit a timeout or needed a
   hand.
5. Before the sessions end, take the third memory sample. Then let them land their changes in
   the plan's order, and note how each landing went.
6. When every session is done, run `upool status`, and read the run's lines from the log: every
   `take`, `open`, `close`, `release` and `refresh`, with its steps and total.

**Each session's brief says:** that its slot is the one it started in, which `upool which`
prints, with nothing to take; to open its Editor with `upool open`, so that the budget counts it
and the log records it; what to do and on which files, and which of those files another session
edits too; for a session that hosts a server, its own port, so that two sessions never share one;
for a session that needs a second peer, how to start it; to commit to its branch; and to stop
once its work has landed, leaving its slot to go back to the pool as its session ends.

## Scrub before anything leaves the project

The record names no project, repository, issue, asset, machine or person. Before it leaves the
project, replace every name with its shape: "a Unity 6000.4 project with a 30 GB Library, three
sessions, two editing the same prefab" is the level of detail. A path is written as `~` with no
user name in it; the upool repository's `make check-paths` refuses a home-directory path anywhere
in a tracked file. Search the record for the project's name, its repository's, each session's
branch and asset names, the machine's name and your own before you send it.

## Where the results go

One record per run, from the template in `docs/benchmarking.md` of the upool repository, by pull
request to [weslo/upool](https://github.com/weslo/upool), as a new file under `docs/benchmarks/`.
