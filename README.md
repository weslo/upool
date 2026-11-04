# upool

[![ci](https://github.com/weslo/upool/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/weslo/upool/actions/workflows/ci.yml?query=branch%3Amain)

**Status: pre-alpha.** v0.6.1, the pool core, its Claude Code integration, Editor governance,
the pool's hygiene and the step timings its benchmarking reads, is on Homebrew, as the Quickstart
installs it, and on [GitHub Releases](https://github.com/weslo/upool/releases). The
[roadmap](#-roadmap) shows where the build stands.

upool keeps a small pool of pre-warmed Unity workspaces for each project and hands one to every
coding-agent session, so the agent's Unity Editor opens in seconds instead of re-importing the
project for minutes. It returns each workspace to the pool when the session ends and keeps the
number of running Editors within your machine's memory.

## 🚀 Quickstart

```bash
brew install weslo/tap/upool
cd <project> && upool init
```

`upool init` runs once per Unity project, from Terminal, inside the repository, with the project's
Unity Editor closed. It makes the project a pool and clones its first slot from your checkout, then
registers upool's hooks in the project's `.claude/settings.local.json`, all in that project alone,
and touches no other repository. It writes nothing into your `CLAUDE.md`, `AGENTS.md` or skills.
Upgrade with `brew upgrade upool`, then run `upool init` again in each project to refresh them; that
also takes out the block and skill an earlier upool wrote there, except from a file git tracks,
which it names for you to change in a commit.

With the project under `~/Documents`, the first session the desktop app starts there may make
macOS ask whether upool may access your Documents folder, since the app's helper runs upool's hook
in its own name. macOS records your answer for upool's binary by its path, and each Homebrew
upgrade puts upool at a new path (`…/Cellar/upool/<version>/bin/upool`), so it asks again after
every upgrade. So `upool init` raises that prompt itself, as its last step, for you to answer while
you are at the keyboard, and `upool doctor` raises it in its `documents-access` check, saying while
it is open and passing once you allow it: run `upool doctor` in a project after every upgrade. A
session that starts while the prompt is open waits on it, with nothing on the screen but the
prompt, until the app gives up after five minutes.

## 🏊 How a session gets a slot

- Start a Claude Code session with a worktree in a project you ran `upool init` in; it opens in a
  ready slot under the project's `.claude/worktrees/` within a couple of seconds, and the pool is
  topped up in the background. With every slot taken, it waits up to 20 seconds for one, then fails
  to start, saying to run `upool refresh`. The top-up, and each `take`, `release`, `refresh`,
  `open` and `close` you run, logs how long each of its steps took in
  `~/.upool/pools/<repo-key>/timings.jsonl`; the commands also say so on stderr.
- The slot is a full copy of your checkout, Library and agent files included, so Unity opens it in
  well under a minute instead of re-importing everything. Gitignored files you want kept current,
  such as local notes, go in a `.worktreeinclude`, as for Claude Code's own worktrees: upool
  copies your checkout's into the free slots before each session.
- upool's hooks tell the session, and its subagents, how to work in the slot, writing nothing into
  your agent files, and the session's `unity` commands reach the slot's own Editor, never another
  session's.
- When you archive the session, the slot goes back to the pool; if it holds uncommitted work, or a
  commit no branch holds, it is parked for you to look at, never reset.
- Subagents and repositories without a pool get ordinary git worktrees. Once a subagent's session
  is gone, `upool prune` lists its worktree and `upool prune --yes` removes it, unless it holds
  work.

## 📋 Requirements

- macOS on an APFS volume
- [Unity CLI](https://docs.unity.com/en-us/unity-cli) 1.0.0-beta.13 or later
- [Claude Code](https://code.claude.com) 2.1.295 or later
- git

## 🧭 Roadmap

After the bootstrap and a planning pass, the build runs in five milestones, M0 to M4, in the order
[docs/design.md](docs/design.md#build-in-four-phases-with-the-clone-recipe-first) sets, and M5 takes
the work moved out of M4. Each milestone ends with a manual gate run on a real Mac, and its version
is tagged only once that gate passes.

Windows is next. Its milestone starts with a spike on a Windows machine
([#261](https://github.com/weslo/upool/issues/261)), and the rest is planned from what the spike
measures. [#262](https://github.com/weslo/upool/issues/262) tracks it through to a release on
winget, whose gate runs on Windows.

| Status | Milestone | What it delivers | Release |
|---|---|---|---|
| ✅ Done | Bootstrap ([#1](https://github.com/weslo/upool/pull/1), [#7](https://github.com/weslo/upool/pull/7)) | The repository: design documents, spike captures, CI and the release pipeline | — |
| ✅ Done | Planning ([#2](https://github.com/weslo/upool/issues/2), [#10](https://github.com/weslo/upool/pull/10)) | Each milestone below split into issues, listed in the [Build plan](https://github.com/weslo/upool/issues/107) | — |
| ✅ Done | [M0 Scaffold](https://github.com/weslo/upool/milestone/1) | Foundations with no user-facing commands: config, state files under one lock, the output package and the test harness | — |
| ✅ Done | [M1 Pool core](https://github.com/weslo/upool/milestone/2) | `init`, `take`, `release`, `which`, `path`, `status` and `doctor`, usable from Terminal with no agent | v0.1.0 |
| ✅ Done | [M2 Claude Code integration](https://github.com/weslo/upool/milestone/3) | `init`'s per-project hooks that hand each session a slot, the background top-up, the skill and `run` | v0.2.0 |
| ✅ Done | [M3 Editor governance](https://github.com/weslo/upool/milestone/4) | The RAM budget for running Editors, `open` and `close`, and the lane hook | v0.3.0 |
| ✅ Done | [M4 Hygiene and release](https://github.com/weslo/upool/milestone/5) | `refresh --fetch`, `prune`, `status --du` and the disk budget, then the Homebrew release the Quickstart uses | v0.4.0 |
| ✅ Done | [M5](https://github.com/weslo/upool/milestone/6) | Work moved out of M4: a slot stuck when a desktop session ends without `WorktreeRemove` ([#192](https://github.com/weslo/upool/issues/192)) | v0.5.0 |
| 🚧 In progress | [Early Benchmarking](https://github.com/weslo/upool/milestone/8) | Step timings for every slot and Editor operation, and the `upool-benchmark` skill with the run record ([#266](https://github.com/weslo/upool/issues/266), [#267](https://github.com/weslo/upool/issues/267)); the first run's record is [docs/benchmarks/2026-11-03-three-sessions-shared-prefab.md](docs/benchmarks/2026-11-03-three-sessions-shared-prefab.md) | v0.6.1 |
| 🔜 Next | [🪟 Windows Compatibility](https://github.com/weslo/upool/milestone/7) | upool on Windows, installed from winget, starting with a spike that measures what the design rests on there ([#261](https://github.com/weslo/upool/issues/261)) | — |

## 📚 Documentation

- [docs/design.md](docs/design.md): what upool is, why, and the build order.
- [docs/contracts.md](docs/contracts.md): the Claude Code and Unity CLI behaviour upool depends on.
- [docs/spike-report.md](docs/spike-report.md): the measurements behind both.
- [docs/benchmarking.md](docs/benchmarking.md): how upool is benchmarked on a project's own body of
  work, and the record each run adds. The `upool-benchmark` skill runs it; install it in the
  project with `npx skills add weslo/upool --skill upool-benchmark`.
- [CONTRIBUTING.md](CONTRIBUTING.md): how to build, test and send changes, and the coding standards.
- [AGENTS.md](AGENTS.md): what a coding agent working on this repository must know.

## 📄 License

[MIT](LICENSE)
